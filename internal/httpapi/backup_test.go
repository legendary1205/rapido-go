package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"
)

// fakeDump replaces the real pg_dump exec for tests that only care about
// the surrounding HTTP/retention/path-traversal logic - see
// dumpDatabaseFn's doc comment in backup.go for why this isn't "mocking
// business logic".
func fakeDump(content string) dumpDatabaseFn {
	return func(ctx context.Context, w *os.File) error {
		gz := gzip.NewWriter(w)
		if _, err := gz.Write([]byte(content)); err != nil {
			return err
		}
		return gz.Close()
	}
}

func decodeBackupList(t *testing.T, raw []byte) []backupInfo {
	t.Helper()
	var backups []backupInfo
	if err := json.Unmarshal(raw, &backups); err != nil {
		t.Fatalf("decode backup list: %v (raw=%s)", err, raw)
	}
	return backups
}

func TestListBackupsEmptyWhenNoneExist(t *testing.T) {
	router, token, _ := newTestRouterAndHandler(t)
	resp := doRequest(t, router, "GET", "/api/settings/backup", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("list backups: %d %s", resp.Code, resp.Raw)
	}
	backups := decodeBackupList(t, resp.Raw)
	if len(backups) != 0 {
		t.Errorf("backups = %v, want empty", backups)
	}
}

func TestCreateBackupProducesRealGzippedContent(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	handler.dumpDatabase = fakeDump("-- fake sql dump\n")

	createResp := doRequest(t, router, "POST", "/api/settings/backup", token, nil)
	if createResp.Code != http.StatusOK {
		t.Fatalf("create backup: %d %s", createResp.Code, createResp.Raw)
	}
	var created backupInfo
	if err := json.Unmarshal(createResp.Raw, &created); err != nil {
		t.Fatalf("decode created backup: %v", err)
	}
	if !backupFilenamePattern.MatchString(created.Filename) {
		t.Errorf("filename %q does not match the expected pattern", created.Filename)
	}
	if created.SizeBytes <= 0 {
		t.Errorf("size_bytes = %d, want > 0", created.SizeBytes)
	}

	listResp := doRequest(t, router, "GET", "/api/settings/backup", token, nil)
	backups := decodeBackupList(t, listResp.Raw)
	if len(backups) != 1 || backups[0].Filename != created.Filename {
		t.Fatalf("backups = %v, want exactly the just-created one", backups)
	}

	downloadResp := doRequest(t, router, "GET", "/api/settings/backup/"+created.Filename, token, nil)
	if downloadResp.Code != http.StatusOK {
		t.Fatalf("download backup: %d %s", downloadResp.Code, downloadResp.Raw)
	}
	gz, err := gzip.NewReader(bytes.NewReader(downloadResp.Raw))
	if err != nil {
		t.Fatalf("downloaded backup is not valid gzip: %v", err)
	}
	defer gz.Close()
	var out bytes.Buffer
	if _, err := out.ReadFrom(gz); err != nil {
		t.Fatalf("read gzip content: %v", err)
	}
	if out.String() != "-- fake sql dump\n" {
		t.Errorf("decompressed content = %q, want the fake dump content", out.String())
	}
}

func TestCreateBackupFailurePropagatesPgDumpError(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	handler.dumpDatabase = func(ctx context.Context, w *os.File) error {
		return errors.New("simulated pg_dump failure")
	}

	resp := doRequest(t, router, "POST", "/api/settings/backup", token, nil)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("create backup with failing dump: %d %s, want 500", resp.Code, resp.Raw)
	}

	listResp := doRequest(t, router, "GET", "/api/settings/backup", token, nil)
	backups := decodeBackupList(t, listResp.Raw)
	if len(backups) != 0 {
		t.Errorf("backups = %v, want none - a failed dump must not leave a partial file listed", backups)
	}
}

func TestBackupRetentionKeepsOnlyMostRecent(t *testing.T) {
	_, _, handler := newTestRouterAndHandler(t)
	handler.backupKeep = 2

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var filenames []string
	for i := 0; i < 4; i++ {
		name := newBackupFilename(base.Add(time.Duration(i) * time.Minute))
		filenames = append(filenames, name)
		f, err := os.Create(handler.backupDir + string(os.PathSeparator) + name)
		if err != nil {
			t.Fatalf("create fixture backup %d: %v", i, err)
		}
		f.Close()
	}

	handler.pruneOldBackups()

	backups, err := listBackupsSorted(handler.backupDir)
	if err != nil {
		t.Fatalf("listBackupsSorted: %v", err)
	}
	if len(backups) != 2 {
		t.Fatalf("backups after pruning = %v, want exactly 2", backups)
	}
	// The two most recent (index 2 and 3) must survive; the two oldest must be gone.
	want := map[string]bool{filenames[2]: true, filenames[3]: true}
	for _, b := range backups {
		if !want[b.Filename] {
			t.Errorf("unexpected survivor %q, oldest backups should have been pruned", b.Filename)
		}
	}
}

func TestBackupDownloadRejectsPathTraversal(t *testing.T) {
	router, token, _ := newTestRouterAndHandler(t)
	for _, filename := range []string{
		"../../etc/passwd",
		"..%2F..%2Fetc%2Fpasswd",
		"rapido_20260101T000000.000Z.sql.gz/../../etc/passwd",
		"not-a-backup-filename.txt",
	} {
		resp := doRequest(t, router, "GET", "/api/settings/backup/"+filename, token, nil)
		if resp.Code == http.StatusOK {
			t.Errorf("filename %q: got 200, want it rejected before touching the filesystem", filename)
		}
	}
}

func TestBackupDeleteRejectsPathTraversalAndRemovesRealFile(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)

	badResp := doRequest(t, router, "DELETE", "/api/settings/backup/..%2F..%2Fetc%2Fpasswd", token, nil)
	if badResp.Code == http.StatusOK {
		t.Errorf("path traversal delete: got 200, want rejected")
	}

	handler.dumpDatabase = fakeDump("x")
	createResp := doRequest(t, router, "POST", "/api/settings/backup", token, nil)
	var created backupInfo
	json.Unmarshal(createResp.Raw, &created)

	delResp := doRequest(t, router, "DELETE", "/api/settings/backup/"+created.Filename, token, nil)
	if delResp.Code != http.StatusOK {
		t.Fatalf("delete backup: %d %s", delResp.Code, delResp.Raw)
	}

	listResp := doRequest(t, router, "GET", "/api/settings/backup", token, nil)
	if backups := decodeBackupList(t, listResp.Raw); len(backups) != 0 {
		t.Errorf("backups after delete = %v, want empty", backups)
	}

	missingResp := doRequest(t, router, "DELETE", "/api/settings/backup/"+created.Filename, token, nil)
	if missingResp.Code != http.StatusNotFound {
		t.Errorf("deleting an already-deleted backup: %d, want 404", missingResp.Code)
	}
}

func TestBackupEndpointsRequireSudo(t *testing.T) {
	router, _, _ := newTestRouterAndHandler(t)
	for _, req := range []struct{ method, path string }{
		{"GET", "/api/settings/backup"},
		{"POST", "/api/settings/backup"},
		{"GET", "/api/settings/backup/rapido_20260101T000000.000Z.sql.gz"},
		{"DELETE", "/api/settings/backup/rapido_20260101T000000.000Z.sql.gz"},
	} {
		resp := doRequest(t, router, req.method, req.path, "", nil)
		if resp.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with no token: %d, want 401", req.method, req.path, resp.Code)
		}
	}
}

// TestRealPgDumpProducesAValidDump is the one test in this file that
// exercises the actual pg_dump binary end to end against the real test
// Postgres - skipped wherever pg_dump isn't installed (the Windows dev
// machine), matching internal/hostmetrics's pattern of only proving the
// real OS-level integration on a host that actually has it; the Linux test
// server (where pg_dump is provisioned as part of this phase's deploy)
// closes that gap for real.
func TestRealPgDumpProducesAValidDump(t *testing.T) {
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Skip("pg_dump not installed, skipping real-binary test")
	}
	router, token, _ := newTestRouterAndHandler(t)

	resp := doRequest(t, router, "POST", "/api/settings/backup", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("create backup with real pg_dump: %d %s", resp.Code, resp.Raw)
	}
	var created backupInfo
	if err := json.Unmarshal(resp.Raw, &created); err != nil {
		t.Fatalf("decode created backup: %v", err)
	}

	downloadResp := doRequest(t, router, "GET", "/api/settings/backup/"+created.Filename, token, nil)
	gz, err := gzip.NewReader(bytes.NewReader(downloadResp.Raw))
	if err != nil {
		t.Fatalf("real pg_dump output is not valid gzip: %v", err)
	}
	defer gz.Close()
	var out bytes.Buffer
	if _, err := out.ReadFrom(gz); err != nil {
		t.Fatalf("read gzip content: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("PostgreSQL database dump")) {
		t.Errorf("decompressed content does not look like a real pg_dump SQL dump: first 200 bytes: %q", firstN(out.Bytes(), 200))
	}
}

func firstN(b []byte, n int) []byte {
	if len(b) < n {
		return b
	}
	return b[:n]
}

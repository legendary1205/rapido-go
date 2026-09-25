package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/legendary1205/rapido-go/internal/hostmetrics"
)

const testSecret = "0123456789abcdef0123456789abcdef"

type fileWrite struct {
	path string
	mode os.FileMode
}

// cliHost stands in for systemd, the package manager and the clock.
type cliHost struct {
	t      *testing.T
	dir    string
	c      *cli
	out    *bytes.Buffer
	errOut *bytes.Buffer

	mu      sync.Mutex
	clock   time.Time
	calls   []string
	writes  []fileWrite
	missing map[string]bool

	exe string
	// state is the unit's is-active answer; journal is what journalctl prints.
	state   func() string
	journal func() string
	// override answers a command line first; handled=false falls through.
	override func(line string) (out string, err error, handled bool)
	tunnels  []hostmetrics.Tunnel
}

func newCLIHost(t *testing.T) *cliHost {
	t.Helper()
	dir := t.TempDir()
	h := &cliHost{
		t: t, dir: dir, out: &bytes.Buffer{}, errOut: &bytes.Buffer{},
		clock:   time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC),
		missing: map[string]bool{},
		state:   func() string { return "active" },
		exe:     filepath.Join(dir, "download", "rapido-go-node"),
	}
	h.journal = h.listeningJournal
	if err := os.MkdirAll(filepath.Dir(h.exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.exe, []byte("NODE-BINARY-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.c = &cli{
		out: h.out, errOut: h.errOut,
		paths: cliPaths{
			Bin:          filepath.Join(dir, "usr", "local", "bin", "rapido-go-node"),
			Env:          filepath.Join(dir, "etc", "rapido-node.env"),
			Unit:         filepath.Join(dir, "etc", "systemd", "system", unitName),
			Sysctl:       filepath.Join(dir, "etc", "sysctl.d", "99-rapido-node.conf"),
			CertDir:      filepath.Join(dir, "etc", "rapido-node"),
			WireGuardDir: filepath.Join(dir, "etc", "wireguard"),
			BBRList:      filepath.Join(dir, "proc", "tcp_available_congestion_control"),
		},
		goos: "linux", goarch: "amd64",
		euid:  func() int { return 0 },
		now:   h.now,
		sleep: h.sleepFor,
		run:   h.runCmd,
		lookPath: func(name string) (string, error) {
			if h.missing[name] {
				return "", exec.ErrNotFound
			}
			return "/usr/bin/" + name, nil
		},
		writeFile:    h.write,
		executable:   func() (string, error) { return h.exe, nil },
		httpClient:   http.DefaultClient,
		tunnelStatus: func(context.Context) []hostmetrics.Tunnel { return h.tunnels },
	}
	return h
}

func (h *cliHost) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clock
}

func (h *cliHost) sleepFor(d time.Duration) {
	h.mu.Lock()
	h.clock = h.clock.Add(d)
	h.mu.Unlock()
}

func (h *cliHost) write(path string, data []byte, mode os.FileMode) error {
	h.mu.Lock()
	h.writes = append(h.writes, fileWrite{path, mode})
	h.mu.Unlock()
	return writeFileAtomic(path, data, mode)
}

func (h *cliHost) listeningJournal() string {
	return fmt.Sprintf(`{"time":%q,"level":"INFO","msg":"listening","addr":"0.0.0.0:62051"}`+"\n", h.clock.Format(time.RFC3339Nano))
}

func (h *cliHost) runCmd(ctx context.Context, name string, args ...string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	h.calls = append(h.calls, line)
	if h.override != nil {
		if out, err, ok := h.override(line); ok {
			return out, err
		}
	}
	switch {
	case line == "systemctl is-active "+unitName:
		if s := h.state(); s != "active" {
			return s + "\n", errors.New("exit status 3")
		}
		return "active\n", nil
	case strings.HasPrefix(line, "systemctl is-enabled"):
		return "enabled\n", nil
	case strings.HasPrefix(line, "journalctl"):
		return h.journal(), nil
	}
	return "", nil
}

func (h *cliHost) called(prefix string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, l := range h.calls {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func (h *cliHost) writesTo(path string) []fileWrite {
	h.mu.Lock()
	defer h.mu.Unlock()
	var ws []fileWrite
	for _, w := range h.writes {
		if w.path == path {
			ws = append(ws, w)
		}
	}
	return ws
}

func (h *cliHost) read(path string) string {
	h.t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		h.t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func (h *cliHost) exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// cliPanel serves the node build the way the real panel does.
type cliPanel struct {
	*httptest.Server
	mu         sync.Mutex
	binary     []byte
	sumOf      []byte // digest published for the binary; nil = the real one
	binaryGets int
	rejectAuth bool
}

func newCLIPanel(t *testing.T, binary string) *cliPanel {
	p := &cliPanel{binary: []byte(binary)}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.Close)
	return p
}

func (p *cliPanel) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rejectAuth || r.Header.Get("Authorization") != "Bearer "+testSecret {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"detail":"Invalid node report secret"}`)
		return
	}
	switch r.URL.Path {
	case "/install/node/version":
		fmt.Fprint(w, `{"version":"test"}`)
	case "/install/node/amd64.sha256":
		digest := p.sumOf
		if digest == nil {
			s := sha256.Sum256(p.binary)
			digest = s[:]
		}
		fmt.Fprintf(w, "%s  rapido-go-node-linux-amd64\n", hex.EncodeToString(digest))
	case "/install/node/amd64":
		p.binaryGets++
		w.Write(p.binary)
	default:
		http.NotFound(w, r)
	}
}

func (p *cliPanel) blob(t *testing.T) string {
	return encodeTestBlob(t, nodeSetupBlob{Cert: "CERT", Key: "KEY", CA: "CA", Secret: testSecret, PanelURL: p.URL})
}

// seedInstalled lays down what a finished install leaves behind.
func (h *cliHost) seedInstalled(t *testing.T, p *cliPanel, binary string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(h.c.paths.Bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.c.paths.Bin, []byte(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(h.c.paths.Env), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.c.paths.Env, []byte(renderEnvFile(p.blob(t), defaultListenAddr, 0, "")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (h *cliHost) run(args ...string) int { return h.c.dispatch(args) }

func TestInstallWritesEverythingAndStartsTheService(t *testing.T) {
	p := newCLIPanel(t, "unused")
	h := newCLIHost(t)
	blob := p.blob(t)

	if code := h.run("install", "--setup-blob", blob); code != 0 {
		t.Fatalf("install exited %d\nout: %s\nerr: %s", code, h.out, h.errOut)
	}

	wantEnv := "# Written by \"rapido-go-node install\"; re-run the install command to change it.\n" +
		"NODE_SETUP_BLOB=" + blob + "\n" +
		"NODE_LISTEN_ADDR=0.0.0.0:62051\n"
	if got := h.read(h.c.paths.Env); got != wantEnv {
		t.Errorf("env file =\n%q\nwant\n%q", got, wantEnv)
	}
	wantUnit := `[Unit]
Description=Rapido-Go node agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/rapido-node.env
ExecStart=/usr/local/bin/rapido-go-node
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`
	if got := h.read(h.c.paths.Unit); got != wantUnit {
		t.Errorf("unit file =\n%q\nwant\n%q", got, wantUnit)
	}
	if got := h.read(h.c.paths.Bin); got != "NODE-BINARY-1" {
		t.Errorf("installed binary = %q, want a copy of the running executable", got)
	}
	for path, mode := range map[string]os.FileMode{h.c.paths.Env: 0o600, h.c.paths.Unit: 0o644, h.c.paths.Bin: 0o755, h.c.paths.Sysctl: 0o644} {
		ws := h.writesTo(path)
		if len(ws) != 1 || ws[0].mode != mode {
			t.Errorf("writes to %s = %v, want exactly one with mode %o", path, ws, mode)
		}
	}
	if got := h.read(filepath.Join(h.c.paths.CertDir, "key.pem")); got != "KEY" {
		t.Errorf("key.pem = %q, want the blob's key", got)
	}

	// Order matters: the unit must be reloaded before it is enabled and started.
	var systemctl []string
	for _, l := range h.calls {
		if strings.HasPrefix(l, "systemctl daemon-reload") || strings.HasPrefix(l, "systemctl enable") || strings.HasPrefix(l, "systemctl restart") {
			systemctl = append(systemctl, l)
		}
	}
	want := []string{"systemctl daemon-reload", "systemctl enable " + unitName, "systemctl restart " + unitName}
	if strings.Join(systemctl, "|") != strings.Join(want, "|") {
		t.Errorf("systemctl calls = %v, want %v", systemctl, want)
	}
	if h.called("sysctl --system") != 1 {
		t.Errorf("sysctl --system calls = %d, want 1", h.called("sysctl --system"))
	}
	if !strings.Contains(h.out.String(), "Node is installed and connected") {
		t.Errorf("output lacks the success line:\n%s", h.out)
	}
}

func TestRenderEnvFile(t *testing.T) {
	cases := []struct {
		name     string
		interval int
		panelURL string
		want     string
	}{
		{"minimal", 0, "", "# Written by \"rapido-go-node install\"; re-run the install command to change it.\nNODE_SETUP_BLOB=QUJD\nNODE_LISTEN_ADDR=0.0.0.0:9000\n"},
		{"with interval", 5, "", "# Written by \"rapido-go-node install\"; re-run the install command to change it.\nNODE_SETUP_BLOB=QUJD\nNODE_LISTEN_ADDR=0.0.0.0:9000\nNODE_REPORT_INTERVAL_SECONDS=5\n"},
		{"with panel url", 0, "https://p.example.com", "# Written by \"rapido-go-node install\"; re-run the install command to change it.\nNODE_SETUP_BLOB=QUJD\nNODE_LISTEN_ADDR=0.0.0.0:9000\nPANEL_URL=https://p.example.com\n"},
	}
	for _, tc := range cases {
		if got := renderEnvFile("QUJD", "0.0.0.0:9000", tc.interval, tc.panelURL); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRenderSysctl(t *testing.T) {
	base := "# Written by \"rapido-go-node install\"; skip with --no-tune.\nnet.core.somaxconn = 4096\nnet.ipv4.tcp_max_syn_backlog = 4096\nnet.ipv4.tcp_fastopen = 3\n"
	if got := renderSysctl(false); got != base {
		t.Errorf("without bbr = %q", got)
	}
	if got, want := renderSysctl(true), base+"net.core.default_qdisc = fq\nnet.ipv4.tcp_congestion_control = bbr\n"; got != want {
		t.Errorf("with bbr = %q", got)
	}
}

func TestInstallNoTuneWritesNoSysctl(t *testing.T) {
	p := newCLIPanel(t, "unused")
	h := newCLIHost(t)
	if code := h.run("install", "--setup-blob", p.blob(t), "--no-tune"); code != 0 {
		t.Fatalf("install exited %d\n%s\n%s", code, h.out, h.errOut)
	}
	if h.exists(h.c.paths.Sysctl) {
		t.Error("--no-tune still wrote the sysctl file")
	}
	if n := h.called("sysctl") + h.called("modprobe"); n != 0 {
		t.Errorf("--no-tune still ran %d sysctl/modprobe commands", n)
	}
}

func TestInstallTuningUsesBBRWhenTheKernelHasIt(t *testing.T) {
	cases := []struct {
		name     string
		bbrList  string // content of the kernel's list; "" = file absent
		modprobe error
		wantBBR  bool
	}{
		{"loaded", "reno cubic bbr", errors.New("unused"), true},
		{"module available", "", nil, true},
		{"no bbr at all", "reno cubic", errors.New("modprobe: not found"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newCLIPanel(t, "unused")
			h := newCLIHost(t)
			if tc.bbrList != "" {
				os.MkdirAll(filepath.Dir(h.c.paths.BBRList), 0o755)
				os.WriteFile(h.c.paths.BBRList, []byte(tc.bbrList+"\n"), 0o644)
			}
			h.override = func(line string) (string, error, bool) {
				if strings.HasPrefix(line, "modprobe") {
					return "", tc.modprobe, true
				}
				return "", nil, false
			}
			if code := h.run("install", "--setup-blob", p.blob(t)); code != 0 {
				t.Fatalf("install exited %d\n%s\n%s", code, h.out, h.errOut)
			}
			if got := h.read(h.c.paths.Sysctl); got != renderSysctl(tc.wantBBR) {
				t.Errorf("sysctl file = %q, want the bbr=%v rendering", got, tc.wantBBR)
			}
		})
	}
}

func TestInstallIsIdempotentAndKeepsEarlierSettings(t *testing.T) {
	p := newCLIPanel(t, "unused")
	h := newCLIHost(t)
	blob := p.blob(t)

	if code := h.run("install", "--setup-blob", blob, "--listen-addr", "0.0.0.0:9000", "--report-interval", "5"); code != 0 {
		t.Fatalf("first install exited %d\n%s\n%s", code, h.out, h.errOut)
	}
	first := h.read(h.c.paths.Env)
	binWrites := len(h.writesTo(h.c.paths.Bin))

	// Second run: from the installed binary, no flags at all - the upgrade path.
	h.exe = h.c.paths.Bin
	if code := h.run("install"); code != 0 {
		t.Fatalf("second install exited %d\n%s\n%s", code, h.out, h.errOut)
	}
	if got := h.read(h.c.paths.Env); got != first {
		t.Errorf("re-running install changed the env file:\n%q\nwas\n%q", got, first)
	}
	if !strings.Contains(first, "NODE_LISTEN_ADDR=0.0.0.0:9000\n") || !strings.Contains(first, "NODE_REPORT_INTERVAL_SECONDS=5\n") {
		t.Errorf("first install did not record the given settings: %q", first)
	}
	if got := len(h.writesTo(h.c.paths.Bin)); got != binWrites {
		t.Errorf("binary was rewritten although it already runs from %s (%d -> %d writes)", h.c.paths.Bin, binWrites, got)
	}
	if got := h.called("systemctl restart " + unitName); got != 2 {
		t.Errorf("restarts = %d, want one per install run (2)", got)
	}

	// An explicit flag wins over what was there.
	if code := h.run("install", "--listen-addr", "0.0.0.0:7000"); code != 0 {
		t.Fatalf("third install exited %d\n%s\n%s", code, h.out, h.errOut)
	}
	if got := h.read(h.c.paths.Env); !strings.Contains(got, "NODE_LISTEN_ADDR=0.0.0.0:7000\n") {
		t.Errorf("--listen-addr was ignored on re-install: %q", got)
	}
}

func TestInstallRejectsBadInputBeforeTouchingTheHost(t *testing.T) {
	p := newCLIPanel(t, "unused")
	noPanel := encodeTestBlob(t, nodeSetupBlob{Cert: "c", Key: "k", CA: "ca", Secret: testSecret})
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"not base64", []string{"--setup-blob", "not base64!!!"}, "base64"},
		{"missing fields", []string{"--setup-blob", encodeTestBlob(t, nodeSetupBlob{Cert: "c", Key: "k"})}, "missing"},
		{"newline injection", []string{"--setup-blob", p.blob(t) + "\nEVIL=1"}, "base64"},
		{"no blob at all", nil, "--setup-blob is required"},
		{"no panel address", []string{"--setup-blob", noPanel}, "--panel-url"},
		{"bad listen addr", []string{"--setup-blob", p.blob(t), "--listen-addr", "nonsense"}, "--listen-addr"},
		{"bad port", []string{"--setup-blob", p.blob(t), "--listen-addr", "0.0.0.0:99999"}, "port"},
		{"bad panel url", []string{"--setup-blob", noPanel, "--panel-url", "ftp://x"}, "panel URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newCLIHost(t)
			if code := h.run(append([]string{"install"}, tc.args...)...); code != 1 {
				t.Fatalf("exit = %d, want 1\n%s", code, h.errOut)
			}
			if !strings.Contains(h.errOut.String(), tc.want) {
				t.Errorf("error %q does not mention %q", h.errOut, tc.want)
			}
			for _, path := range []string{h.c.paths.Env, h.c.paths.Unit, h.c.paths.Bin} {
				if h.exists(path) {
					t.Errorf("%s was written despite the rejected input", path)
				}
			}
			if len(h.calls) != 0 {
				t.Errorf("commands ran despite the rejected input: %v", h.calls)
			}
		})
	}
}

func TestInstallUsesPanelURLFlagOnlyWhenTheBlobHasNone(t *testing.T) {
	h := newCLIHost(t)
	p := newCLIPanel(t, "unused")
	noPanel := encodeTestBlob(t, nodeSetupBlob{Cert: "c", Key: "k", CA: "ca", Secret: testSecret})
	if code := h.run("install", "--setup-blob", noPanel, "--panel-url", p.URL+"/"); code != 0 {
		t.Fatalf("install exited %d\n%s\n%s", code, h.out, h.errOut)
	}
	if got := h.read(h.c.paths.Env); !strings.Contains(got, "PANEL_URL="+p.URL+"\n") {
		t.Errorf("env file lacks the normalized PANEL_URL: %q", got)
	}

	// With a blob that already names its panel, the flag is not recorded.
	h2 := newCLIHost(t)
	if code := h2.run("install", "--setup-blob", p.blob(t), "--panel-url", "https://other.example.com"); code != 0 {
		t.Fatalf("install exited %d\n%s\n%s", code, h2.out, h2.errOut)
	}
	if got := h2.read(h2.c.paths.Env); strings.Contains(got, "PANEL_URL") {
		t.Errorf("blob panel_url should win, but the env file has %q", got)
	}
}

func TestInstallNeedsRootAndLinux(t *testing.T) {
	p := newCLIPanel(t, "unused")
	h := newCLIHost(t)
	h.c.euid = func() int { return 1000 }
	if code := h.run("install", "--setup-blob", p.blob(t)); code != 1 || !strings.Contains(h.errOut.String(), "root") {
		t.Errorf("non-root: code %d, err %q", code, h.errOut)
	}

	h = newCLIHost(t)
	h.c.goos = "windows"
	if code := h.run("install", "--setup-blob", p.blob(t)); code != 1 || !strings.Contains(h.errOut.String(), "Linux") {
		t.Errorf("windows: code %d, err %q", code, h.errOut)
	}
	if code := h.run("version"); code != 0 {
		t.Errorf("version must work on every OS, exit %d", code)
	}

	h = newCLIHost(t)
	h.missing["systemctl"] = true
	if code := h.run("install", "--setup-blob", p.blob(t)); code != 1 || !strings.Contains(h.errOut.String(), "systemd") {
		t.Errorf("no systemd: code %d, err %q", code, h.errOut)
	}
}

func TestInstallReportsTheJournalWhenTheServiceFails(t *testing.T) {
	p := newCLIPanel(t, "unused")
	h := newCLIHost(t)
	h.state = func() string { return "failed" }
	h.journal = func() string { return "panic: load node certificate: bad pem\n" }

	if code := h.run("install", "--setup-blob", p.blob(t)); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	out := h.out.String()
	for _, want := range []string{"the service is failed", "Last log lines", "load node certificate: bad pem"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !h.exists(h.c.paths.Unit) {
		t.Error("a failed start should leave the installed files in place for inspection")
	}
}

func TestInstallFailsWhenThePanelRejectsTheSecret(t *testing.T) {
	p := newCLIPanel(t, "unused")
	p.rejectAuth = true
	h := newCLIHost(t)
	if code := h.run("install", "--setup-blob", p.blob(t)); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(h.out.String(), "rejected this node's secret") {
		t.Errorf("output does not explain the rejection:\n%s", h.out)
	}
}

func TestInstallFailsWhenTheAgentLogsSyncFailures(t *testing.T) {
	p := newCLIPanel(t, "unused")
	h := newCLIHost(t)
	h.journal = func() string {
		now := h.clock.Format(time.RFC3339Nano)
		return fmt.Sprintf(`{"time":%q,"level":"INFO","msg":"listening","addr":"0.0.0.0:62051"}`+"\n"+
			`{"time":%q,"level":"WARN","msg":"pull config: panel rejected request","status":403}`+"\n", now, now)
	}
	if code := h.run("install", "--setup-blob", p.blob(t)); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(h.out.String(), "panel rejected request") {
		t.Errorf("output lacks the agent's own complaint:\n%s", h.out)
	}
}

func TestInstallWarnsAboutAnActiveFirewallAndMissingWireGuardTools(t *testing.T) {
	p := newCLIPanel(t, "unused")
	h := newCLIHost(t)
	os.MkdirAll(h.c.paths.WireGuardDir, 0o755)
	os.WriteFile(filepath.Join(h.c.paths.WireGuardDir, "wg0.conf"), []byte("[Interface]\n"), 0o600)
	h.missing["wg"] = true
	h.override = func(line string) (string, error, bool) {
		if line == "ufw status" {
			return "Status: active\n", nil, true
		}
		return "", nil, false
	}
	if code := h.run("install", "--setup-blob", p.blob(t)); code != 0 {
		t.Fatalf("install exited %d\n%s\n%s", code, h.out, h.errOut)
	}
	out := h.out.String()
	if !strings.Contains(out, "ufw is active") || !strings.Contains(out, "wireguard-tools is not installed") {
		t.Errorf("missing warnings:\n%s", out)
	}
	if h.called("ufw allow") != 0 {
		t.Error("install must never open firewall ports by itself")
	}
}

func TestUpdateHappyPath(t *testing.T) {
	p := newCLIPanel(t, "NODE-BINARY-2")
	h := newCLIHost(t)
	h.seedInstalled(t, p, "NODE-BINARY-1")

	if code := h.run("update"); code != 0 {
		t.Fatalf("update exited %d\nout: %s\nerr: %s", code, h.out, h.errOut)
	}
	if got := h.read(h.c.paths.Bin); got != "NODE-BINARY-2" {
		t.Errorf("binary = %q, want the new build", got)
	}
	backup := h.c.paths.Bin + ".bak-20260925-120000"
	if got := h.read(backup); got != "NODE-BINARY-1" {
		t.Errorf("backup = %q, want the previous build", got)
	}
	if h.called("systemctl restart "+unitName) != 1 {
		t.Errorf("restarts = %d, want 1", h.called("systemctl restart "+unitName))
	}
	entries, _ := os.ReadDir(filepath.Dir(h.c.paths.Bin))
	for _, e := range entries {
		if strings.Contains(e.Name(), "download") {
			t.Errorf("temp download %s was left behind", e.Name())
		}
	}
}

func TestUpdateSkipsWhenAlreadyCurrent(t *testing.T) {
	p := newCLIPanel(t, "NODE-BINARY-1")
	h := newCLIHost(t)
	h.seedInstalled(t, p, "NODE-BINARY-1")

	if code := h.run("update"); code != 0 {
		t.Fatalf("update exited %d\n%s\n%s", code, h.out, h.errOut)
	}
	if p.binaryGets != 0 {
		t.Errorf("downloaded the binary %d times although it is identical", p.binaryGets)
	}
	if h.called("systemctl") != 0 {
		t.Errorf("the service was touched although nothing changed: %v", h.calls)
	}
	if !strings.Contains(h.out.String(), "already up to date") {
		t.Errorf("output: %s", h.out)
	}
}

func TestUpdateAbortsOnChecksumMismatch(t *testing.T) {
	p := newCLIPanel(t, "NODE-BINARY-2")
	wrong := sha256.Sum256([]byte("something else"))
	p.sumOf = wrong[:]
	h := newCLIHost(t)
	h.seedInstalled(t, p, "NODE-BINARY-1")

	if code := h.run("update"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(h.errOut.String(), "checksum mismatch") {
		t.Errorf("error: %s", h.errOut)
	}
	if got := h.read(h.c.paths.Bin); got != "NODE-BINARY-1" {
		t.Errorf("binary changed to %q despite the mismatch", got)
	}
	if h.called("systemctl") != 0 {
		t.Errorf("the service was touched: %v", h.calls)
	}
	entries, _ := os.ReadDir(filepath.Dir(h.c.paths.Bin))
	if len(entries) != 1 {
		t.Errorf("bin dir should hold only the binary, has %d entries", len(entries))
	}
}

func TestUpdateAbortsWhenTheNewBuildFailsItsSmokeTest(t *testing.T) {
	p := newCLIPanel(t, "NODE-BINARY-2")
	h := newCLIHost(t)
	h.seedInstalled(t, p, "NODE-BINARY-1")
	h.override = func(line string) (string, error, bool) {
		if strings.Contains(line, ".rapido-go-node.download-") && strings.HasSuffix(line, " version") {
			return "exec format error", errors.New("exit status 126"), true
		}
		return "", nil, false
	}

	if code := h.run("update"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(h.errOut.String(), "self-test") {
		t.Errorf("error: %s", h.errOut)
	}
	if got := h.read(h.c.paths.Bin); got != "NODE-BINARY-1" {
		t.Errorf("binary changed to %q", got)
	}
	if h.called("systemctl") != 0 {
		t.Errorf("the service was touched: %v", h.calls)
	}
}

func TestUpdateRollsBackWhenTheServiceDoesNotComeUp(t *testing.T) {
	p := newCLIPanel(t, "NODE-BINARY-2")
	h := newCLIHost(t)
	h.seedInstalled(t, p, "NODE-BINARY-1")
	// The new build crash-loops; the old one is healthy.
	h.state = func() string {
		if raw, _ := os.ReadFile(h.c.paths.Bin); string(raw) == "NODE-BINARY-2" {
			return "activating"
		}
		return "active"
	}

	code := h.run("update")
	if code != 1 {
		t.Fatalf("exit = %d, want non-zero after a rollback", code)
	}
	if got := h.read(h.c.paths.Bin); got != "NODE-BINARY-1" {
		t.Errorf("binary = %q, want the previous build restored", got)
	}
	if h.called("systemctl restart "+unitName) != 2 {
		t.Errorf("restarts = %d, want 2 (new build, then the rollback)", h.called("systemctl restart "+unitName))
	}
	if !strings.Contains(h.errOut.String(), "rolled back") {
		t.Errorf("error: %s", h.errOut)
	}
}

func TestUpdateRollbackFailureIsReported(t *testing.T) {
	p := newCLIPanel(t, "NODE-BINARY-2")
	h := newCLIHost(t)
	h.seedInstalled(t, p, "NODE-BINARY-1")
	h.state = func() string { return "failed" }

	if code := h.run("update"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(h.errOut.String(), "still not running after the rollback") {
		t.Errorf("error: %s", h.errOut)
	}
}

func TestUpdateKeepsOnlyTheNewestThreeBackups(t *testing.T) {
	p := newCLIPanel(t, "NODE-BINARY-2")
	h := newCLIHost(t)
	h.seedInstalled(t, p, "NODE-BINARY-1")
	old := []string{"20260101-000000", "20260102-000000", "20260103-000000", "20260104-000000", "20260105-000000"}
	for _, stamp := range old {
		if err := os.WriteFile(h.c.paths.Bin+".bak-"+stamp, []byte(stamp), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if code := h.run("update"); code != 0 {
		t.Fatalf("update exited %d\n%s\n%s", code, h.out, h.errOut)
	}
	matches, _ := filepath.Glob(h.c.paths.Bin + ".bak-*")
	var got []string
	for _, m := range matches {
		got = append(got, strings.TrimPrefix(m, h.c.paths.Bin+".bak-"))
	}
	want := []string{"20260104-000000", "20260105-000000", "20260925-120000"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("backups kept = %v, want %v", got, want)
	}
}

func TestUpdateExplainsARejectedSecretAndAMissingInstall(t *testing.T) {
	p := newCLIPanel(t, "NODE-BINARY-2")
	p.rejectAuth = true
	h := newCLIHost(t)
	h.seedInstalled(t, p, "NODE-BINARY-1")
	if code := h.run("update"); code != 1 || !strings.Contains(h.errOut.String(), "rejected this node's secret") {
		t.Errorf("rejected secret: code %d, err %q", code, h.errOut)
	}

	h = newCLIHost(t)
	if code := h.run("update"); code != 1 || !strings.Contains(h.errOut.String(), "run install first") {
		t.Errorf("no install: code %d, err %q", code, h.errOut)
	}
}

func TestUninstallKeepsCertificatesUnlessPurged(t *testing.T) {
	for _, purge := range []bool{false, true} {
		t.Run(fmt.Sprintf("purge=%v", purge), func(t *testing.T) {
			p := newCLIPanel(t, "unused")
			h := newCLIHost(t)
			if code := h.run("install", "--setup-blob", p.blob(t)); code != 0 {
				t.Fatalf("install exited %d\n%s\n%s", code, h.out, h.errOut)
			}
			os.WriteFile(h.c.paths.Bin+".bak-20260101-000000", []byte("old"), 0o755)

			args := []string{"uninstall"}
			if purge {
				args = append(args, "--purge")
			}
			if code := h.run(args...); code != 0 {
				t.Fatalf("uninstall exited %d\n%s\n%s", code, h.out, h.errOut)
			}
			for _, path := range []string{h.c.paths.Unit, h.c.paths.Env, h.c.paths.Sysctl, h.c.paths.Bin, h.c.paths.Bin + ".bak-20260101-000000"} {
				if h.exists(path) {
					t.Errorf("%s survived uninstall", path)
				}
			}
			if h.called("systemctl disable --now "+unitName) != 1 {
				t.Error("the service was not stopped and disabled")
			}
			if got := h.exists(h.c.paths.CertDir); got == purge {
				t.Errorf("cert dir exists = %v with purge = %v", got, purge)
			}
		})
	}
}

func TestFixWireGuardConf(t *testing.T) {
	const mullvad = "[Interface]\nPrivateKey = k\nAddress = 10.0.0.2/32\nDNS = 10.64.0.1\n\n[Peer]\nPublicKey = p\nEndpoint = 1.2.3.4:51820\nAllowedIPs = 0.0.0.0/0\n"
	cases := []struct {
		name     string
		in       string
		resolved bool
		want     string
		dns      bool
		added    int
	}{
		{
			name: "no resolver, no keepalive", in: mullvad,
			want: "[Interface]\nPrivateKey = k\nAddress = 10.0.0.2/32\n#DNS = 10.64.0.1\n\n[Peer]\nPublicKey = p\nEndpoint = 1.2.3.4:51820\nAllowedIPs = 0.0.0.0/0\nPersistentKeepalive = 25\n",
			dns:  true, added: 1,
		},
		{
			name: "resolver running keeps DNS", in: mullvad, resolved: true,
			want:  strings.Replace(mullvad, "AllowedIPs = 0.0.0.0/0\n", "AllowedIPs = 0.0.0.0/0\nPersistentKeepalive = 25\n", 1),
			added: 1,
		},
		{
			name: "already fine is untouched", in: "[Interface]\nPrivateKey = k\n\n[Peer]\nPublicKey = p\npersistentkeepalive = 10\n", resolved: true,
			want: "[Interface]\nPrivateKey = k\n\n[Peer]\nPublicKey = p\npersistentkeepalive = 10\n",
		},
		{
			name: "every peer without one gets one, placed before the blank lines", in: "[Interface]\nPrivateKey = k\n[Peer]\nPublicKey = a\n\n[Peer]\nPublicKey = b\nPersistentKeepalive = 30\n[Peer]\nPublicKey = c\n\n\n", resolved: true,
			want:  "[Interface]\nPrivateKey = k\n[Peer]\nPublicKey = a\nPersistentKeepalive = 25\n\n[Peer]\nPublicKey = b\nPersistentKeepalive = 30\n[Peer]\nPublicKey = c\nPersistentKeepalive = 25\n",
			added: 2,
		},
		{
			name: "a commented keepalive does not count", in: "[Peer]\nPublicKey = a\n#PersistentKeepalive = 25\n", resolved: true,
			want:  "[Peer]\nPublicKey = a\n#PersistentKeepalive = 25\nPersistentKeepalive = 25\n",
			added: 1,
		},
		{
			name: "no peer section means nothing to add", in: "[Interface]\nPrivateKey = k\n", resolved: true,
			want: "[Interface]\nPrivateKey = k\n",
		},
	}
	for _, tc := range cases {
		got := fixWireGuardConf(tc.in, tc.resolved)
		if got.Content != tc.want || got.CommentedDNS != tc.dns || got.AddedKeepalive != tc.added {
			t.Errorf("%s:\ncontent %q\nwant    %q\ndns=%v added=%d (want %v/%d)", tc.name, got.Content, tc.want, got.CommentedDNS, got.AddedKeepalive, tc.dns, tc.added)
		}
	}
}

func TestTunnelsBringsUpFixesAndEnablesEveryConfig(t *testing.T) {
	h := newCLIHost(t)
	os.MkdirAll(h.c.paths.WireGuardDir, 0o755)
	conf := filepath.Join(h.c.paths.WireGuardDir, "wg0.conf")
	os.WriteFile(conf, []byte("[Interface]\nPrivateKey = k\nDNS = 10.64.0.1\n\n[Peer]\nPublicKey = p\nAllowedIPs = 0.0.0.0/0\n"), 0o600)
	os.WriteFile(filepath.Join(h.c.paths.WireGuardDir, "notes.txt"), []byte("ignored"), 0o600)
	age := 7.0
	wantMode := os.FileMode(0o600)
	if fi, err := os.Stat(conf); err == nil {
		wantMode = fi.Mode().Perm() // the OS's own view: Windows reports 0666
	}
	h.tunnels = []hostmetrics.Tunnel{{Name: "wg0", Up: true, Present: true, Peers: []hostmetrics.Peer{{LastHandshakeAgeSeconds: &age}}}}
	h.override = func(line string) (string, error, bool) {
		switch {
		case line == "ip link show wg0":
			return "", errors.New("Device does not exist"), true
		case strings.HasPrefix(line, "curl") && strings.Contains(line, "--interface wg0"):
			return "203.0.113.9", nil, true
		case line == "systemctl is-active systemd-resolved":
			return "inactive\n", errors.New("exit status 3"), true
		}
		return "", nil, false
	}

	if code := h.run("tunnels"); code != 0 {
		t.Fatalf("tunnels exited %d\n%s\n%s", code, h.out, h.errOut)
	}
	got := h.read(conf)
	if !strings.Contains(got, "#DNS = 10.64.0.1") || !strings.HasSuffix(got, "PersistentKeepalive = 25\n") {
		t.Errorf("config was not repaired:\n%s", got)
	}
	if backups, _ := filepath.Glob(conf + ".bak-*"); len(backups) != 1 {
		t.Errorf("backups of the edited config = %v, want 1", backups)
	}
	if h.called("wg-quick up wg0") != 1 || h.called("systemctl enable wg-quick@wg0") != 1 {
		t.Errorf("calls: %v", h.calls)
	}
	out := h.out.String()
	for _, want := range []string{"wg0", "UP", "handshake 7s ago", "203.0.113.9"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if ws := h.writesTo(conf); len(ws) != 1 || ws[0].mode != wantMode {
		t.Errorf("config rewrites = %v, want one that keeps the original mode %o", ws, wantMode)
	}
}

func TestTunnelsWithNoConfigsFails(t *testing.T) {
	h := newCLIHost(t)
	if code := h.run("tunnels"); code != 1 || !strings.Contains(h.errOut.String(), "no tunnel configs") {
		t.Errorf("code %d, err %q", code, h.errOut)
	}
}

func TestParseSSListeners(t *testing.T) {
	out := `tcp   LISTEN 0      4096       0.0.0.0:62051     0.0.0.0:*    users:(("rapido-go-node",pid=812,fd=7))
tcp   LISTEN 0      4096          [::]:443          [::]:*    users:(("rapido-go-node",pid=812,fd=9))
tcp   LISTEN 0      4096       0.0.0.0:443       0.0.0.0:*    users:(("rapido-go-node",pid=812,fd=8))
udp   UNCONN 0      0          0.0.0.0:8388      0.0.0.0:*    users:(("rapido-go-node",pid=812,fd=10))
tcp   LISTEN 0      128        0.0.0.0:22        0.0.0.0:*    users:(("sshd",pid=1,fd=3))
tcp   LISTEN 0      511          [::]:8443          [::]:*
`
	got := parseSSListeners(out, "rapido-go-node")
	var text []string
	for _, l := range got {
		text = append(text, fmt.Sprintf("%d/%s", l.Port, l.Proto))
	}
	if want := "443/tcp,8388/udp,62051/tcp"; strings.Join(text, ",") != want {
		t.Errorf("listeners = %v, want %s", text, want)
	}
}

func TestAnalyzeAgentLog(t *testing.T) {
	since := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	rec := func(offset time.Duration, level, msg string) string {
		b, _ := json.Marshal(map[string]any{"time": since.Add(offset).Format(time.RFC3339Nano), "level": level, "msg": msg})
		return string(b)
	}
	cases := []struct {
		name       string
		lines      []string
		listening  bool
		pullOK     bool
		failureHas string // "" = no failure expected
	}{
		{"healthy start", []string{rec(time.Second, "INFO", "listening")}, true, false, ""},
		{"old lines are ignored", []string{rec(-time.Minute, "ERROR", "boom"), rec(time.Second, "INFO", "listening")}, true, false, ""},
		{"a tunnel warning is not the installer's business", []string{rec(time.Second, "INFO", "listening"), rec(2*time.Second, "WARN", "wireguard tunnel is down")}, true, false, ""},
		{"sync warning fails it", []string{rec(time.Second, "INFO", "listening"), rec(2*time.Second, "WARN", "push report: request failed, will retry next tick")}, true, false, "push report: request failed"},
		{"an applied pull clears an earlier failure", []string{rec(time.Second, "WARN", "pull config: request failed, will retry next tick"), rec(2*time.Second, "INFO", "pull config: applied full restart"), rec(time.Second, "INFO", "listening")}, true, true, ""},
		{"any error fails it", []string{rec(time.Second, "ERROR", "load node certificate")}, false, false, "load node certificate"},
		{"plain text lines are skipped", []string{"panic: something", rec(time.Second, "INFO", "listening")}, true, false, ""},
	}
	for _, tc := range cases {
		got := analyzeAgentLog(parseAgentLog(strings.Join(tc.lines, "\n")), since)
		failed := got.Failure != ""
		if got.Listening != tc.listening || got.PullOK != tc.pullOK || failed != (tc.failureHas != "") || !strings.Contains(got.Failure, tc.failureHas) {
			t.Errorf("%s: got %+v, want listening=%v pullOK=%v failure containing %q", tc.name, got, tc.listening, tc.pullOK, tc.failureHas)
		}
	}
}

func TestStatusPrintsTheWholePicture(t *testing.T) {
	p := newCLIPanel(t, "unused")
	h := newCLIHost(t)
	h.seedInstalled(t, p, "NODE-BINARY-1")
	h.journal = func() string {
		return `{"time":"2026-09-25T12:00:00Z","level":"INFO","msg":"pull config: hot-applied user list","tag":"in1","users":3}` + "\n"
	}
	probe := 41.0
	age := 12.0
	h.tunnels = []hostmetrics.Tunnel{
		{Name: "wg0", Up: true, Present: true, ProbeMs: &probe, Peers: []hostmetrics.Peer{{LastHandshakeAgeSeconds: &age}}},
		{Name: "wg1", Up: false, Present: false, Error: "interface not found"},
	}
	h.override = func(line string) (string, error, bool) {
		if strings.HasPrefix(line, "ss ") {
			return `tcp LISTEN 0 4096 0.0.0.0:62051 0.0.0.0:* users:(("rapido-go-node",pid=1,fd=3))` + "\n" +
				`tcp LISTEN 0 4096 0.0.0.0:443 0.0.0.0:* users:(("rapido-go-node",pid=1,fd=4))` + "\n", nil, true
		}
		return "", nil, false
	}

	if code := h.run("status"); code != 0 {
		t.Fatalf("status exited %d\n%s\n%s", code, h.out, h.errOut)
	}
	out := h.out.String()
	for _, want := range []string{
		"active (enabled)", "answers, node secret accepted", p.URL,
		"pull config: hot-applied user list tag=in1 users=3",
		"wg0", "UP", "probe 41 ms", "handshake 12s ago", "wg1", "interface missing",
		"443/tcp, 62051/tcp (control)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output lacks %q:\n%s", want, out)
		}
	}

	h.state = func() string { return "inactive" }
	if code := h.run("status"); code != 1 {
		t.Errorf("status of a stopped service exited %d, want 1", code)
	}
}

func TestVersionOutput(t *testing.T) {
	h := newCLIHost(t)
	if code := h.run("version"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimSpace(h.out.String()), "\n")
	if len(lines) != 3 || lines[0] != "rapido-go-node "+version || !strings.HasSuffix(lines[1], "linux/amd64") || lines[2] != singBoxVersion {
		t.Errorf("version output = %q", h.out)
	}
}

func TestRunCLIWithNoArgumentsLeavesTheAgentToRun(t *testing.T) {
	if runCLI(nil) {
		t.Fatal("runCLI(nil) claimed the invocation; systemd starts the agent with no arguments")
	}
}

func TestUnknownCommandShowsUsage(t *testing.T) {
	h := newCLIHost(t)
	if code := h.run("frobnicate"); code != 2 || !strings.Contains(h.errOut.String(), "unknown command") {
		t.Errorf("code %d, err %q", code, h.errOut)
	}
	if code := h.run("install", "--nope"); code != 2 {
		t.Errorf("bad flag: code %d, want 2", code)
	}
	if code := h.run("status", "extra"); code != 2 {
		t.Errorf("stray argument: code %d, want 2", code)
	}
}

func TestParseChecksum(t *testing.T) {
	good := strings.Repeat("ab", 32)
	if got, err := parseChecksum(strings.ToUpper(good) + "  rapido-go-node-linux-amd64\n"); err != nil || got != good {
		t.Errorf("got %q, %v", got, err)
	}
	for _, bad := range []string{"", "not a digest", strings.Repeat("a", 63), `{"detail":"Not Found"}`} {
		if _, err := parseChecksum(bad); err == nil {
			t.Errorf("parseChecksum(%q) accepted garbage", bad)
		}
	}
}

// The agent decodes the blob with applyNodeSetupBlob on every start; the
// installer must accept exactly what that accepts.
func TestInstallAcceptsWhatTheAgentAccepts(t *testing.T) {
	p := newCLIPanel(t, "unused")
	for _, blob := range []string{p.blob(t), encodeTestBlob(t, nodeSetupBlob{Cert: "c", Key: "k", CA: "ca", Secret: "s", PanelURL: p.URL})} {
		h := newCLIHost(t)
		agent := config{CertFile: filepath.Join(h.dir, "a", "c"), KeyFile: filepath.Join(h.dir, "a", "k"), CAFile: filepath.Join(h.dir, "a", "ca")}
		if err := applyNodeSetupBlob(blob, &agent); err != nil {
			t.Fatalf("the agent rejects a blob this test built: %v", err)
		}
		h.run("install", "--setup-blob", blob) // the second blob's secret is unknown to the fake panel, so only the files matter
		if !h.exists(h.c.paths.Env) || !h.exists(h.c.paths.Unit) {
			t.Errorf("installer rejected a blob the agent accepts:\n%s\n%s", h.out, h.errOut)
		}
	}
}

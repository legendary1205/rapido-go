package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/legendary1205/rapido-go/internal/hostmetrics"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	unitName          = "rapido-go-node.service"
	defaultBinPath    = "/usr/local/bin/rapido-go-node"
	defaultEnvPath    = "/etc/rapido-node.env"
	defaultListenAddr = "0.0.0.0:62051"
	keepBackups       = 3
)

// cliPaths are every file the management commands touch. Tests point them at a
// temp directory; the systemd unit text itself always names the canonical paths.
type cliPaths struct {
	Bin, Env, Unit, Sysctl, CertDir, WireGuardDir, BBRList string
}

func defaultCLIPaths() cliPaths {
	return cliPaths{
		Bin:          defaultBinPath,
		Env:          defaultEnvPath,
		Unit:         "/etc/systemd/system/" + unitName,
		Sysctl:       "/etc/sysctl.d/99-rapido-node.conf",
		CertDir:      "/etc/rapido-node",
		WireGuardDir: "/etc/wireguard",
		BBRList:      "/proc/sys/net/ipv4/tcp_available_congestion_control",
	}
}

// cli is the management side of the binary. Everything with a side effect on
// the host goes through a field here so the tests can stand in for systemd.
type cli struct {
	out, errOut io.Writer
	paths       cliPaths
	goos        string
	goarch      string

	euid       func() int
	now        func() time.Time
	sleep      func(time.Duration)
	run        func(ctx context.Context, name string, args ...string) (string, error)
	lookPath   func(string) (string, error)
	writeFile  func(path string, data []byte, mode os.FileMode) error
	executable func() (string, error)
	httpClient *http.Client

	// tunnelStatus reports every WireGuard tunnel with one round of health probes.
	tunnelStatus func(ctx context.Context) []hostmetrics.Tunnel
}

func newCLI(out, errOut io.Writer) *cli {
	return &cli{
		out: out, errOut: errOut, paths: defaultCLIPaths(),
		goos: runtime.GOOS, goarch: runtime.GOARCH,
		euid: os.Geteuid, now: time.Now, sleep: time.Sleep,
		run: execRun, lookPath: exec.LookPath, writeFile: writeFileAtomic,
		executable: os.Executable, httpClient: &http.Client{},
		tunnelStatus: realTunnelStatus,
	}
}

// runCLI runs a management subcommand. It reports false when args is empty so
// main carries on as the agent - systemd starts the service with no arguments.
func runCLI(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if code := newCLI(os.Stdout, os.Stderr).dispatch(args); code != 0 {
		os.Exit(code)
	}
	return true
}

var (
	errUsage     = errors.New("usage")
	errHelpShown = errors.New("help shown")
)

func (c *cli) dispatch(args []string) int {
	cmd, rest := args[0], args[1:]
	commands := map[string]func([]string) error{
		"install":   c.cmdInstall,
		"update":    c.cmdUpdate,
		"uninstall": c.cmdUninstall,
		"status":    c.cmdStatus,
		"tunnels":   c.cmdTunnels,
	}
	switch cmd {
	case "version", "-v", "--version":
		c.printVersion()
		return 0
	case "help", "-h", "--help":
		c.usage(c.out)
		return 0
	}
	fn, ok := commands[cmd]
	if !ok {
		fmt.Fprintf(c.errOut, "unknown command %q\n\n", cmd)
		c.usage(c.errOut)
		return 2
	}
	if c.goos != "linux" {
		fmt.Fprintf(c.errOut, "error: %q needs Linux with systemd (this is %s)\n", cmd, c.goos)
		return 1
	}
	switch err := fn(rest); {
	case err == nil, errors.Is(err, errHelpShown):
		return 0
	case errors.Is(err, errUsage):
		return 2
	default:
		fmt.Fprintln(c.errOut, "error:", err)
		return 1
	}
}

func (c *cli) usage(w io.Writer) {
	fmt.Fprint(w, `rapido-go-node - Rapido-Go node agent

Run with no arguments it is the agent itself (what the systemd service does).

Commands (Linux + systemd, run as root):
  install --setup-blob <b64> [--listen-addr ADDR] [--panel-url URL]
          [--report-interval SECONDS] [--no-tune]
                     install or upgrade in place, then wait for the panel link
  update             download the current build from the panel, verify, swap and
                     restart; rolls back if the service does not come up
  uninstall [--purge]
                     stop and remove the service; --purge also removes certificates
  status             service, panel link, WireGuard tunnels, listening ports
  tunnels            bring up every /etc/wireguard/*.conf and enable it at boot
  version            print version information
`)
}

func (c *cli) printVersion() {
	fmt.Fprintf(c.out, "rapido-go-node %s\n%s %s/%s\n%s\n", version, runtime.Version(), c.goos, c.goarch, singBoxVersion)
}

func (c *cli) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("rapido-go-node "+name, flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	return fs
}

func (c *cli) parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return errHelpShown
		}
		return errUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(c.errOut, "unexpected argument %q\n", fs.Arg(0))
		return errUsage
	}
	return nil
}

func (c *cli) step(format string, a ...any) { fmt.Fprintf(c.out, "==> "+format+"\n", a...) }
func (c *cli) info(format string, a ...any) { fmt.Fprintf(c.out, "    "+format+"\n", a...) }
func (c *cli) warn(format string, a ...any) { fmt.Fprintf(c.out, "WARNING: "+format+"\n", a...) }

// requireHost fails unless this is root on a machine with systemd.
func (c *cli) requireHost() error {
	if c.euid() != 0 {
		return errors.New("this command must run as root (prefix it with sudo)")
	}
	if _, err := c.lookPath("systemctl"); err != nil {
		return errors.New("systemd was not found on this machine; the node agent is installed as a systemd service")
	}
	return nil
}

func execRun(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// sh runs one command with a generous timeout and returns its combined output.
func (c *cli) sh(name string, args ...string) (string, error) {
	return c.shTimeout(2*time.Minute, name, args...)
}

func (c *cli) shTimeout(d time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return c.run(ctx, name, args...)
}

// shErr is sh, turning a failure into an error that carries the command output.
func (c *cli) shErr(name string, args ...string) error {
	out, err := c.sh(name, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return nil
}

func (c *cli) serviceState() string {
	out, _ := c.sh("systemctl", "is-active", unitName)
	if s := strings.TrimSpace(out); s != "" {
		return strings.SplitN(s, "\n", 2)[0]
	}
	return "unknown"
}

// writeFileAtomic replaces path in one rename, so a reader (or a crash) never
// sees a half-written file. The temp file is private until it has its final mode.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var (
	blobCharset = regexp.MustCompile(`^[A-Za-z0-9+/=]+$`)
	sha256Hex   = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

// parseChecksum reads the first field of a `sha256sum`-style line.
func parseChecksum(text string) (string, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 || !sha256Hex.MatchString(fields[0]) {
		return "", errors.New("the checksum file does not hold a SHA-256 digest")
	}
	return strings.ToLower(fields[0]), nil
}

// decodeSetupBlob only reads a blob (no validation, no files) - for commands
// that need the panel URL and secret of an already installed node.
func decodeSetupBlob(blob string) (nodeSetupBlob, error) {
	var b nodeSetupBlob
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return b, fmt.Errorf("not valid base64: %w", err)
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return b, fmt.Errorf("not a valid setup blob: %w", err)
	}
	return b, nil
}

// parseEnvFile reads KEY=VALUE lines the way systemd's EnvironmentFile does
// for the simple values this installer writes.
func parseEnvFile(text string) map[string]string {
	env := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		env[strings.TrimSpace(k)] = v
	}
	return env
}

func (c *cli) readEnv() map[string]string {
	raw, err := os.ReadFile(c.paths.Env)
	if err != nil {
		return nil
	}
	return parseEnvFile(string(raw))
}

// installedTarget is where this node reports to and the secret it proves it
// with, read from what install wrote.
func (c *cli) installedTarget() (panelURL, secret string, err error) {
	env := c.readEnv()
	if env == nil || env["NODE_SETUP_BLOB"] == "" {
		return "", "", fmt.Errorf("no installation found (%s is missing) - run install first", c.paths.Env)
	}
	b, err := decodeSetupBlob(env["NODE_SETUP_BLOB"])
	if err != nil {
		return "", "", fmt.Errorf("the installed setup blob is unreadable: %w", err)
	}
	panelURL = strings.TrimRight(firstNonEmpty(b.PanelURL, env["PANEL_URL"]), "/")
	if panelURL == "" || b.Secret == "" {
		return "", "", errors.New("the installation has no panel URL or secret - re-run the install command from the panel")
	}
	return panelURL, b.Secret, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func renderEnvFile(blob, listenAddr string, reportIntervalSeconds int, panelURL string) string {
	var b strings.Builder
	b.WriteString("# Written by \"rapido-go-node install\"; re-run the install command to change it.\n")
	b.WriteString("NODE_SETUP_BLOB=" + blob + "\n")
	b.WriteString("NODE_LISTEN_ADDR=" + listenAddr + "\n")
	if reportIntervalSeconds > 0 {
		b.WriteString("NODE_REPORT_INTERVAL_SECONDS=" + strconv.Itoa(reportIntervalSeconds) + "\n")
	}
	if panelURL != "" {
		b.WriteString("PANEL_URL=" + panelURL + "\n")
	}
	return b.String()
}

func renderUnit() string {
	return `[Unit]
Description=Rapido-Go node agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=` + defaultEnvPath + `
ExecStart=` + defaultBinPath + `
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`
}

func renderSysctl(bbr bool) string {
	s := `# Written by "rapido-go-node install"; skip with --no-tune.
net.core.somaxconn = 4096
net.ipv4.tcp_max_syn_backlog = 4096
net.ipv4.tcp_fastopen = 3
`
	if bbr {
		s += "net.core.default_qdisc = fq\nnet.ipv4.tcp_congestion_control = bbr\n"
	}
	return s
}

// bbrAvailable is whether the kernel can use BBR: already loaded, or a module
// that modprobe can find.
func (c *cli) bbrAvailable() bool {
	if raw, err := os.ReadFile(c.paths.BBRList); err == nil {
		for _, f := range strings.Fields(string(raw)) {
			if f == "bbr" {
				return true
			}
		}
	}
	_, err := c.sh("modprobe", "-n", "-q", "tcp_bbr")
	return err == nil
}

func validListenAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--listen-addr %q must look like 0.0.0.0:62051", addr)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("--listen-addr %q: the port must be 1-65535", addr)
	}
	if strings.ContainsAny(host, " \t\r\n\"'\\$") {
		return fmt.Errorf("--listen-addr %q has invalid characters", addr)
	}
	return nil
}

func normalizePanelURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(raw, " \t\r\n\"'\\$") {
		return "", fmt.Errorf("panel URL %q must look like https://panel.example.com", raw)
	}
	return raw, nil
}

type installOptions struct {
	Blob           string
	ListenAddr     string
	PanelURL       string
	ReportInterval int
	NoTune         bool
}

func (c *cli) cmdInstall(args []string) error {
	fs := c.flags("install")
	var o installOptions
	fs.StringVar(&o.Blob, "setup-blob", "", "the setup blob from the panel's Nodes page (optional when upgrading an existing install)")
	fs.StringVar(&o.ListenAddr, "listen-addr", "", "control-plane listen address (default "+defaultListenAddr+")")
	fs.StringVar(&o.PanelURL, "panel-url", "", "panel address, used only when the blob carries none")
	fs.IntVar(&o.ReportInterval, "report-interval", 0, "seconds between reports to the panel (default 10)")
	fs.BoolVar(&o.NoTune, "no-tune", false, "do not install the network sysctl tuning")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if err := c.requireHost(); err != nil {
		return err
	}
	return c.install(o)
}

func (c *cli) install(o installOptions) error {
	existing := c.readEnv()

	blob := strings.TrimSpace(o.Blob)
	if blob == "" {
		blob = existing["NODE_SETUP_BLOB"]
	}
	if blob == "" {
		return errors.New("--setup-blob is required: copy the install command from the panel's Nodes page")
	}
	// base64 decoding skips newlines, and this value is written into an env
	// file line - a stray newline would inject another variable.
	if !blobCharset.MatchString(blob) {
		return errors.New("the setup blob holds characters that are not valid base64; copy it again without line breaks")
	}

	listen := firstNonEmpty(o.ListenAddr, existing["NODE_LISTEN_ADDR"], defaultListenAddr)
	if err := validListenAddr(listen); err != nil {
		return err
	}
	if o.ReportInterval < 0 {
		return errors.New("--report-interval must be a positive number of seconds")
	}
	interval := o.ReportInterval
	if interval == 0 {
		if n, err := strconv.Atoi(existing["NODE_REPORT_INTERVAL_SECONDS"]); err == nil && n > 0 {
			interval = n
		}
	}

	// The same decoder and checks the agent runs on every start; it also lays
	// the certificates down, which the agent would do on first boot anyway.
	cfg := config{
		CertFile: filepath.Join(c.paths.CertDir, "cert.pem"),
		KeyFile:  filepath.Join(c.paths.CertDir, "key.pem"),
		CAFile:   filepath.Join(c.paths.CertDir, "ca.pem"),
	}
	if err := applyNodeSetupBlob(blob, &cfg); err != nil {
		return fmt.Errorf("invalid setup blob: %w", err)
	}

	panelURL, envPanelURL := cfg.PanelURL, ""
	if panelURL == "" {
		envPanelURL = firstNonEmpty(o.PanelURL, existing["PANEL_URL"])
		panelURL = envPanelURL
	}
	if panelURL == "" {
		return errors.New("the setup blob carries no panel address; add --panel-url https://your-panel")
	}
	panelURL, err := normalizePanelURL(panelURL)
	if err != nil {
		return err
	}
	if envPanelURL != "" {
		envPanelURL = panelURL
	}

	c.step("Installing the node agent")
	if err := c.installBinary(); err != nil {
		return err
	}
	if err := c.writeFile(c.paths.Env, []byte(renderEnvFile(blob, listen, interval, envPanelURL)), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", c.paths.Env, err)
	}
	if err := c.writeFile(c.paths.Unit, []byte(renderUnit()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", c.paths.Unit, err)
	}
	if o.NoTune {
		c.info("skipping network tuning (--no-tune)")
	} else {
		c.tune()
	}

	c.step("Starting the service")
	started := c.now()
	for _, args := range [][]string{{"daemon-reload"}, {"enable", unitName}, {"restart", unitName}} {
		if err := c.shErr("systemctl", args...); err != nil {
			return err
		}
	}

	c.step("Waiting for the node to reach the panel")
	syncEvery := 10 * time.Second // the agent's default report interval
	if interval > 0 {
		syncEvery = time.Duration(interval) * time.Second
	}
	ok, reason := c.waitForAgent(started, syncEvery, panelURL, cfg.ReportSecret)
	if !ok {
		c.warn("%s", reason)
		c.printJournalTail()
		return errors.New("the service was installed but the node is not healthy yet (see above); fix that and re-run this command")
	}

	c.step("Node is installed and connected")
	c.info("panel   : %s", panelURL)
	c.info("control : %s (the node connects out to the panel; nothing needs to reach this port)", listen)
	c.info("manage  : rapido-go-node status | update | tunnels | uninstall")
	c.postInstallNotes()
	return nil
}

// installBinary puts the running executable at the canonical path, unless it
// already is that file.
func (c *cli) installBinary() error {
	exe, err := c.executable()
	if err != nil {
		return fmt.Errorf("cannot locate the running binary: %w", err)
	}
	if a, err := os.Stat(exe); err == nil {
		if b, err := os.Stat(c.paths.Bin); err == nil && os.SameFile(a, b) {
			c.info("binary already at %s", c.paths.Bin)
			return nil
		}
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return fmt.Errorf("read %s: %w", exe, err)
	}
	if err := c.writeFile(c.paths.Bin, data, 0o755); err != nil {
		return fmt.Errorf("install %s: %w", c.paths.Bin, err)
	}
	c.info("binary installed at %s", c.paths.Bin)
	return nil
}

func (c *cli) tune() {
	bbr := c.bbrAvailable()
	if err := c.writeFile(c.paths.Sysctl, []byte(renderSysctl(bbr)), 0o644); err != nil {
		c.warn("could not write %s: %v", c.paths.Sysctl, err)
		return
	}
	if out, err := c.sh("sysctl", "--system"); err != nil {
		c.warn("sysctl --system failed (continuing): %s", strings.TrimSpace(out))
		return
	}
	if bbr {
		c.info("network tuning applied (BBR + fq)")
	} else {
		c.info("network tuning applied (this kernel has no BBR)")
	}
}

func (c *cli) postInstallNotes() {
	if _, err := c.lookPath("ufw"); err == nil {
		if out, _ := c.sh("ufw", "status"); strings.Contains(out, "Status: active") {
			c.warn("ufw is active. Allow the control port above and every inbound port you set in the panel, or clients cannot connect.")
		}
	}
	confs, _ := filepath.Glob(filepath.Join(c.paths.WireGuardDir, "*.conf"))
	if _, err := c.lookPath("wg"); err != nil && len(confs) > 0 {
		c.warn("%d WireGuard config(s) found but wireguard-tools is not installed; run: rapido-go-node tunnels", len(confs))
	}
}

func (c *cli) printJournalTail() {
	out, err := c.sh("journalctl", "-u", unitName, "-n", "15", "--no-pager", "-o", "cat")
	if err != nil || strings.TrimSpace(out) == "" {
		out, _ = c.sh("systemctl", "status", "--no-pager", "-l", unitName)
	}
	fmt.Fprintln(c.out, "Last log lines:")
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fmt.Fprintln(c.out, "    "+line)
	}
}

// agentLogRecord is one JSON line the agent logged.
type agentLogRecord struct {
	Time  time.Time
	Level string
	Msg   string
	Attrs map[string]any
}

func parseAgentLog(out string) []agentLogRecord {
	var recs []agentLogRecord
	for _, line := range strings.Split(out, "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &m) != nil {
			continue
		}
		msg, _ := m["msg"].(string)
		if msg == "" {
			continue
		}
		rec := agentLogRecord{Msg: msg, Attrs: map[string]any{}}
		rec.Level, _ = m["level"].(string)
		if ts, _ := m["time"].(string); ts != "" {
			rec.Time, _ = time.Parse(time.RFC3339Nano, ts)
		}
		for k, v := range m {
			if k != "time" && k != "level" && k != "msg" {
				rec.Attrs[k] = v
			}
		}
		recs = append(recs, rec)
	}
	return recs
}

func (r agentLogRecord) String() string {
	keys := make([]string, 0, len(r.Attrs))
	for k := range r.Attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := r.Msg
	for _, k := range keys {
		s += fmt.Sprintf(" %s=%v", k, r.Attrs[k])
	}
	if !r.Time.IsZero() {
		s = r.Time.Local().Format("15:04:05") + " " + s
	}
	return s
}

func isSyncRecord(r agentLogRecord) bool {
	return strings.HasPrefix(r.Msg, "pull config") || strings.HasPrefix(r.Msg, "push report")
}

type agentVerdict struct {
	Listening bool
	// PullOK is a pull that changed something - the only success the agent logs.
	PullOK  bool
	Failure string
}

// analyzeAgentLog reads what the agent logged since the given moment. Only
// failures that matter to a fresh install count: an error, or a sync warning
// (an unreachable tunnel is a warning too, but not the installer's business).
func analyzeAgentLog(recs []agentLogRecord, since time.Time) agentVerdict {
	var v agentVerdict
	for _, r := range recs {
		if r.Time.Before(since.Add(-time.Second)) {
			continue
		}
		switch {
		case r.Msg == "listening":
			v.Listening = true
		case strings.HasPrefix(r.Msg, "pull config: applied") || strings.HasPrefix(r.Msg, "pull config: hot-applied"):
			v.PullOK, v.Failure = true, ""
		case strings.EqualFold(r.Level, "ERROR"), strings.EqualFold(r.Level, "WARN") && (isSyncRecord(r) || strings.HasPrefix(r.Msg, "PANEL_URL")):
			v.Failure = r.String()
		}
	}
	return v
}

// panelCheck is what one authenticated request to the panel showed.
type panelCheck struct {
	Err            error
	Status         int
	SecretRejected bool
}

// checkPanel asks the panel for its node-build version with this node's
// secret. The panel answers 401 to a wrong secret before it looks at any file,
// so any other reply means both the address and the secret work.
func (c *cli) checkPanel(panelURL, secret string) panelCheck {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := c.panelGet(ctx, panelURL+"/install/node/version", secret)
	if err != nil {
		return panelCheck{Err: err}
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return panelCheck{Status: resp.StatusCode, SecretRejected: resp.StatusCode == http.StatusUnauthorized}
}

func (c *cli) panelGet(ctx context.Context, rawURL, secret string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	return c.httpClient.Do(req)
}

// waitForAgent gives the freshly started service ~30s to be active, listening
// and on good terms with the panel. The agent logs nothing on a routine
// successful sync, so absence of failures after one sync interval, plus an
// authenticated request of our own, is the evidence of a working link.
func (c *cli) waitForAgent(started time.Time, syncInterval time.Duration, panelURL, secret string) (bool, string) {
	settle := min(syncInterval+2*time.Second, 25*time.Second)
	deadline := started.Add(30 * time.Second)
	reason := "the service is not active"
	for {
		if state := c.serviceState(); state != "active" {
			reason = "the service is " + state
		} else {
			// --since rather than a line count: a busy core can log enough to push
			// the agent's own startup line out of a fixed-size tail.
			out, _ := c.sh("journalctl", "-u", unitName, "--since", started.Local().Format("2006-01-02 15:04:05"), "-n", "2000", "--no-pager", "-o", "cat")
			v := analyzeAgentLog(parseAgentLog(out), started)
			switch {
			case !v.Listening:
				reason = "the agent is running but is not listening yet"
			case v.Failure != "":
				reason = "the agent logged: " + v.Failure
			default:
				chk := c.checkPanel(panelURL, secret)
				switch {
				case chk.Err != nil:
					reason = fmt.Sprintf("cannot reach the panel at %s: %v", panelURL, chk.Err)
				case chk.SecretRejected:
					reason = "the panel rejected this node's secret (was the node deleted or re-created? copy the install command again)"
				case v.PullOK || !c.now().Before(started.Add(settle)):
					return true, ""
				default:
					reason = "still waiting for the first sync with the panel"
				}
			}
		}
		if !c.now().Before(deadline) {
			return false, reason
		}
		c.sleep(time.Second)
	}
}

func (c *cli) cmdUninstall(args []string) error {
	fs := c.flags("uninstall")
	purge := fs.Bool("purge", false, "also remove the node certificates in "+defaultCLIPaths().CertDir)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if c.euid() != 0 {
		return errors.New("this command must run as root (prefix it with sudo)")
	}
	c.step("Removing the node agent")
	if _, err := c.lookPath("systemctl"); err == nil {
		c.sh("systemctl", "disable", "--now", unitName)
	}
	backups, _ := filepath.Glob(c.paths.Bin + ".bak-*")
	targets := append([]string{c.paths.Unit, c.paths.Env, c.paths.Sysctl, c.paths.Bin}, backups...)
	for _, p := range targets {
		switch err := os.Remove(p); {
		case err == nil:
			c.info("removed %s", p)
		case !errors.Is(err, os.ErrNotExist):
			c.warn("could not remove %s: %v", p, err)
		}
	}
	if _, err := c.lookPath("systemctl"); err == nil {
		c.sh("systemctl", "daemon-reload")
	}
	if *purge {
		if err := os.RemoveAll(c.paths.CertDir); err != nil {
			c.warn("could not remove %s: %v", c.paths.CertDir, err)
		} else {
			c.info("removed %s", c.paths.CertDir)
		}
	} else {
		c.info("kept %s (certificates); use --purge to remove them", c.paths.CertDir)
	}
	c.info("network tuning stays active until the next reboot")
	return nil
}

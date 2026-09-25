package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/legendary1205/rapido-go/internal/hostmetrics"
	"github.com/legendary1205/rapido-go/internal/tunnelhealth"
)

func (c *cli) cmdStatus(args []string) error {
	if err := c.parse(c.flags("status"), args); err != nil {
		return err
	}
	return c.status()
}

func (c *cli) status() error {
	panelURL, secret, targetErr := c.installedTarget()
	state := c.serviceState()

	fmt.Fprintln(c.out, "Rapido-Go node agent")
	fmt.Fprintf(c.out, "  version   : %s (%s %s/%s, %s)\n", version, runtime.Version(), c.goos, c.goarch, singBoxVersion)
	enabled := strings.TrimSpace(firstLineOf(c.mustSh("systemctl", "is-enabled", unitName)))
	fmt.Fprintf(c.out, "  service   : %s (%s)\n", state, firstNonEmpty(enabled, "not installed"))

	if targetErr != nil {
		fmt.Fprintf(c.out, "  panel     : unknown - %v\n", targetErr)
	} else {
		fmt.Fprintf(c.out, "  panel     : %s - %s\n", panelURL, describePanelCheck(c.checkPanel(panelURL, secret)))
	}
	fmt.Fprintf(c.out, "  last sync : %s\n", c.lastSyncLine())
	fmt.Fprintf(c.out, "  clients   : %s\n", c.clientsLine())

	c.printTunnelHealth()
	c.printListeners()

	if state != "active" {
		return fmt.Errorf("the service is %s", state)
	}
	return nil
}

func (c *cli) mustSh(name string, args ...string) string {
	out, _ := c.sh(name, args...)
	return out
}

func firstLineOf(s string) string {
	return strings.SplitN(strings.TrimSpace(s), "\n", 2)[0]
}

func describePanelCheck(chk panelCheck) string {
	switch {
	case chk.Err != nil:
		return "not answering (" + chk.Err.Error() + ")"
	case chk.SecretRejected:
		return "answers, but rejects this node's secret"
	case chk.Status == 200:
		return "answers, node secret accepted"
	default:
		return fmt.Sprintf("answers (HTTP %d)", chk.Status)
	}
}

// lastSyncLine is the newest pull/apply/report line the agent logged.
func (c *cli) lastSyncLine() string {
	recs := c.agentSyncRecords()
	for i := len(recs) - 1; i >= 0; i-- {
		if isSyncRecord(recs[i]) {
			return recs[i].String()
		}
	}
	return "nothing logged yet (an unchanged config logs nothing; only changes and failures do)"
}

// journalSyncPattern picks the agent's own sync records (its JSON lines about
// pulling the config and pushing reports) out of the journal.
const journalSyncPattern = `"msg":"(pull config|push report)`

// agentSyncRecords reads the agent's sync records from the journal of the current
// run. A tail of the last N lines is not enough: a node whose core logs thousands
// of lines an hour scrolls the agent's rare lines out of any fixed window. So the
// filter runs inside journalctl (-g), over everything since the service last
// started, and only the matching lines come back. There is deliberately no -n
// next to --since: how journalctl combines the two (which end of the range it
// keeps) differs between versions, while the sync lines alone are few.
func (c *cli) agentSyncRecords() []agentLogRecord {
	args := []string{"-u", unitName, "--no-pager", "-o", "cat"}
	grep := append(slices.Clone(args), "-g", journalSyncPattern)
	if since := c.serviceStartSince(); since != "" {
		grep = append(grep, "--since", since)
	} else {
		grep = append(grep, "-n", "3000")
	}
	out, err := c.sh("journalctl", grep...)
	if !journalGrepUnsupported(out, err) {
		return parseAgentLog(out)
	}
	// journalctl older than v237 (or built without pattern matching) has no -g:
	// read a long tail instead and filter it here.
	out, _ = c.sh("journalctl", append(args, "-n", "50000")...)
	return parseAgentLog(out)
}

// journalGrepUnsupported reports whether journalctl refused the -g option (as
// opposed to running it and finding nothing, which is an answer).
func journalGrepUnsupported(out string, err error) bool {
	if err == nil {
		return false
	}
	out = strings.ToLower(out)
	for _, marker := range []string{"invalid option", "unrecognized option", "unknown option", "pattern matching", "not supported"} {
		if strings.Contains(out, marker) {
			return true
		}
	}
	return false
}

var activeEnterTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}`)

// serviceStartSince is when the unit last became active, as journalctl --since
// wants it ("" when systemd does not say). systemctl prints the local wall-clock
// time with a weekday in front and a zone name behind
// ("Fri 2026-09-25 03:00:00 UTC"); journalctl reads the same local time back.
func (c *cli) serviceStartSince() string {
	out, err := c.sh("systemctl", "show", "-p", "ActiveEnterTimestamp", unitName)
	if err != nil {
		return ""
	}
	return activeEnterTime.FindString(out)
}

// liveStateMaxAge is how old the agent's live snapshot may be and still count as
// current; the agent writes one every few seconds while it runs.
const liveStateMaxAge = 30 * time.Second

// clientsLine is the open-connection count from the agent's own live snapshot -
// no round trip to the panel.
func (c *cli) clientsLine() string {
	const none = "n/a (the agent has not written a live snapshot yet)"
	if c.paths.Live == "" {
		return none
	}
	raw, err := os.ReadFile(c.paths.Live)
	if err != nil {
		return none
	}
	var snap liveSnapshot
	if json.Unmarshal(raw, &snap) != nil || snap.At.IsZero() {
		return none
	}
	age := c.now().Sub(snap.At)
	if age > liveStateMaxAge {
		return "n/a (the last snapshot is " + formatAge(age) + ")"
	}
	return fmt.Sprintf("%d users online, %d connections open", snap.UsersOnline, snap.ConnsTotal)
}

func (c *cli) printTunnelHealth() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tunnels := c.tunnelStatus(ctx)
	if len(tunnels) == 0 {
		fmt.Fprintln(c.out, "  tunnels   : none configured")
		return
	}
	fmt.Fprintln(c.out, "  tunnels   :")
	for _, t := range tunnels {
		fmt.Fprintf(c.out, "    %-16s %s\n", t.Name, describeTunnel(t))
	}
}

func describeTunnel(t hostmetrics.Tunnel) string {
	if !t.Present {
		return "DOWN  interface missing (run: rapido-go-node tunnels)"
	}
	s := "DOWN"
	if t.Up {
		s = "UP"
		if t.ProbeMs != nil {
			s += fmt.Sprintf("    probe %.0f ms", *t.ProbeMs)
		}
	}
	if age, ok := latestHandshake(t); ok {
		s += "  handshake " + formatAge(age)
	} else if len(t.Peers) > 0 {
		s += "  handshake never"
	}
	if !t.Up && t.Error != "" {
		s += "  (" + t.Error + ")"
	}
	return s
}

func latestHandshake(t hostmetrics.Tunnel) (time.Duration, bool) {
	best, found := time.Duration(0), false
	for _, p := range t.Peers {
		if p.LastHandshakeAgeSeconds == nil {
			continue
		}
		d := time.Duration(*p.LastHandshakeAgeSeconds * float64(time.Second))
		if !found || d < best {
			best, found = d, true
		}
	}
	return best, found
}

func formatAge(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}

// realTunnelStatus runs one probe round over every tunnel on this host.
func realTunnelStatus(ctx context.Context) []hostmetrics.Tunnel {
	sample := hostmetrics.Collect(false, "")
	m := tunnelhealth.New(tunnelhealth.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	m.Tick(ctx)
	return hostmetrics.ApplyTunnelHealth(sample.Tunnels, hostmetrics.ConfiguredTunnelNames(), m.Health())
}

type listener struct {
	Proto string
	Port  int
	Addr  string
	// Device is set for a socket pinned to one interface (ss prints its
	// address as 0.0.0.0%wg0:port). Those are the ephemeral sockets a
	// tunnel-bound outbound opens, one per active connection - hundreds on a
	// busy node - not something clients connect to.
	Device bool
}

// parseSSListeners picks the sockets owned by process proc out of
// `ss -H -lntup` output.
func parseSSListeners(out, proc string) []listener {
	seen := map[string]bool{}
	var ls []listener
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.Contains(line, `"`+proc+`"`) {
			continue
		}
		i := strings.LastIndex(f[4], ":")
		if i < 0 {
			continue
		}
		port, err := strconv.Atoi(f[4][i+1:])
		if err != nil {
			continue
		}
		proto := strings.TrimSuffix(f[0], "6")
		key := proto + "/" + strconv.Itoa(port)
		if seen[key] {
			continue
		}
		seen[key] = true
		ls = append(ls, listener{Proto: proto, Port: port, Addr: f[4], Device: strings.Contains(f[4][:i], "%")})
	}
	sort.Slice(ls, func(i, j int) bool {
		if ls[i].Port != ls[j].Port {
			return ls[i].Port < ls[j].Port
		}
		return ls[i].Proto < ls[j].Proto
	})
	return ls
}

func (c *cli) printListeners() {
	if _, err := c.lookPath("ss"); err != nil {
		fmt.Fprintln(c.out, "  listening : unavailable (ss is not installed)")
		return
	}
	out, _ := c.sh("ss", "-H", "-lntup")
	ls := parseSSListeners(out, "rapido-go-node")
	if len(ls) == 0 {
		fmt.Fprintln(c.out, "  listening : nothing (no core running, or run status as root to see it)")
		return
	}
	control := ""
	if _, p, err := net.SplitHostPort(firstNonEmpty(c.readEnv()["NODE_LISTEN_ADDR"], defaultListenAddr)); err == nil {
		control = p
	}
	var parts []string
	tunnelSockets := 0
	for _, l := range ls {
		if l.Device {
			tunnelSockets++
			continue
		}
		part := fmt.Sprintf("%d/%s", l.Port, l.Proto)
		if strconv.Itoa(l.Port) == control && l.Proto == "tcp" {
			part += " (control)"
		}
		parts = append(parts, part)
	}
	line := strings.Join(parts, ", ")
	if tunnelSockets > 0 {
		if line != "" {
			line += " "
		}
		line += fmt.Sprintf("(+%d outbound tunnel sockets)", tunnelSockets)
	}
	fmt.Fprintf(c.out, "  listening : %s\n", line)
}

var (
	wgDNSLine       = regexp.MustCompile(`(?i)^\s*DNS\s*=`)
	wgKeepaliveLine = regexp.MustCompile(`(?i)^\s*PersistentKeepalive\s*=`)
)

type wgFix struct {
	Content        string
	CommentedDNS   bool
	AddedKeepalive int
}

// fixWireGuardConf makes a tunnel config safe to bring up on a server:
//   - DNS= makes wg-quick call resolvconf, which fails hard (and removes the
//     interface it just made) where systemd-resolved is not running - and the
//     node picks an exit by binding to the interface, never via the system
//     resolver, so the line does nothing for it anyway;
//   - without PersistentKeepalive a tunnel only re-handshakes when traffic
//     happens to flow, so a relay that disappears stays "up" and silent.
func fixWireGuardConf(content string, resolvedRunning bool) wgFix {
	var res wgFix
	var out []string
	inPeer, hasKeepalive := false, false
	closePeer := func() {
		if !inPeer || hasKeepalive {
			return
		}
		at := len(out)
		for at > 0 && strings.TrimSpace(out[at-1]) == "" {
			at--
		}
		out = append(out[:at], append([]string{"PersistentKeepalive = 25"}, out[at:]...)...)
		res.AddedKeepalive++
	}
	for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "["):
			closePeer()
			inPeer, hasKeepalive = strings.EqualFold(trimmed, "[Peer]"), false
		case !resolvedRunning && wgDNSLine.MatchString(line):
			line = "#" + line
			res.CommentedDNS = true
		case wgKeepaliveLine.MatchString(line):
			hasKeepalive = true
		}
		out = append(out, line)
	}
	closePeer()
	res.Content = strings.Join(out, "\n") + "\n"
	return res
}

func (c *cli) packageInstall(pkgs ...string) error {
	switch {
	case c.has("apt-get"):
		if err := c.shErr("apt-get", "update", "-y"); err != nil {
			return err
		}
		return c.shErr("env", append([]string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y"}, pkgs...)...)
	case c.has("dnf"):
		return c.shErr("dnf", append([]string{"install", "-y"}, pkgs...)...)
	case c.has("yum"):
		return c.shErr("yum", append([]string{"install", "-y"}, pkgs...)...)
	}
	return errors.New("no supported package manager found (need apt-get, dnf or yum)")
}

func (c *cli) has(bin string) bool {
	_, err := c.lookPath(bin)
	return err == nil
}

var ipv4Text = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,3}){3}$`)

func (c *cli) cmdTunnels(args []string) error {
	if err := c.parse(c.flags("tunnels"), args); err != nil {
		return err
	}
	if err := c.requireHost(); err != nil {
		return err
	}
	confs, _ := filepath.Glob(filepath.Join(c.paths.WireGuardDir, "*.conf"))
	sort.Strings(confs)
	if len(confs) == 0 {
		return fmt.Errorf("no tunnel configs in %s", c.paths.WireGuardDir)
	}
	if !c.has("wg") {
		c.step("Installing wireguard-tools")
		if err := c.packageInstall("wireguard-tools"); err != nil {
			return err
		}
	}

	resolved := strings.TrimSpace(firstLineOf(c.mustSh("systemctl", "is-active", "systemd-resolved"))) == "active"
	c.step("Bringing up %d tunnel(s)", len(confs))
	var names []string
	for _, conf := range confs {
		name := strings.TrimSuffix(filepath.Base(conf), ".conf")
		if c.bringUpTunnel(name, conf, resolved) {
			names = append(names, name)
		}
	}

	c.sleep(3 * time.Second) // let the first handshake happen before reading it
	exitIPs := c.exitIPs(names)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	byName := map[string]hostmetrics.Tunnel{}
	for _, t := range c.tunnelStatus(ctx) {
		byName[t.Name] = t
	}

	c.step("Tunnel state")
	for _, name := range names {
		line := "no status"
		if t, ok := byName[name]; ok {
			line = describeTunnel(t)
		}
		fmt.Fprintf(c.out, "    %-16s %s  exit %s\n", name, line, exitIPs[name])
	}
	c.info("A tunnel that never handshakes usually means the relay is gone.")
	return nil
}

// bringUpTunnel repairs one config, starts the interface and enables it at
// boot. Reports whether the interface is up afterwards.
func (c *cli) bringUpTunnel(name, conf string, resolvedRunning bool) bool {
	raw, err := os.ReadFile(conf)
	if err != nil {
		c.warn("%s: cannot read %s: %v", name, conf, err)
		return false
	}
	fix := fixWireGuardConf(string(raw), resolvedRunning)
	edited := false
	if fix.Content != string(raw) {
		mode := os.FileMode(0o600)
		if fi, err := os.Stat(conf); err == nil {
			mode = fi.Mode().Perm()
		}
		backup := conf + ".bak-" + c.now().UTC().Format("20060102_150405")
		if err := c.writeFile(backup, raw, mode); err != nil {
			c.warn("%s: cannot back up the config, leaving it untouched: %v", name, err)
		} else if err := c.writeFile(conf, []byte(fix.Content), mode); err != nil {
			c.warn("%s: cannot update the config: %v", name, err)
		} else {
			edited = true
			if fix.CommentedDNS {
				c.info("%s: commented out DNS= (systemd-resolved is not running here)", name)
			}
			if fix.AddedKeepalive > 0 {
				c.info("%s: added PersistentKeepalive = 25", name)
			}
		}
	}

	if _, err := c.sh("ip", "link", "show", name); err == nil {
		c.info("%s: already up", name)
		if edited {
			c.info("%s: the repaired config applies the next time this tunnel is restarted", name)
		}
	} else if out, err := c.sh("wg-quick", "up", name); err != nil {
		c.warn("%s: wg-quick failed:", name)
		lines := strings.Split(strings.TrimSpace(out), "\n")
		for _, l := range lines[max(0, len(lines)-4):] {
			fmt.Fprintln(c.out, "        "+l)
		}
		return false
	} else {
		c.info("%s: up", name)
	}
	if out, err := c.sh("systemctl", "enable", "wg-quick@"+name); err != nil {
		c.warn("%s: could not enable wg-quick@%s at boot: %s", name, name, strings.TrimSpace(out))
	}
	return true
}

// exitIPs asks an external service which address each tunnel's traffic
// leaves from, in parallel since a dead tunnel costs the full timeout.
func (c *cli) exitIPs(names []string) map[string]string {
	res := make(map[string]string, len(names))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ip := "no answer"
			if out, err := c.shTimeout(25*time.Second, "curl", "-fsS", "--max-time", "20", "--interface", name, "https://api.ipify.org"); err == nil {
				if s := strings.TrimSpace(out); ipv4Text.MatchString(s) {
					ip = s
				}
			}
			mu.Lock()
			res[name] = ip
			mu.Unlock()
		}()
	}
	wg.Wait()
	return res
}

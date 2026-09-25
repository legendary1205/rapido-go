// Package hostmetrics reads host-level health (CPU/mem/disk/network/
// WireGuard) straight out of /proc and /sys - the Go equivalent of
// rapido-node/metrics.py, shared by both the node agent (reports itself to
// the panel) and the panel process (samples its own machine, matching
// Python's collect_metrics.py:_panel_sample).
//
// Collect returns raw, cumulative counters only - never a rate or a
// percentage. Exactly like the Python original's own doc comment explains:
// turning a cumulative counter into a rate needs two samples and the real
// gap between them, and only the *receiving* side (the panel, which is
// long-lived and already stores every previous sample) knows that gap
// reliably. A node computing its own rate would silently be wrong after
// any missed or delayed push. See rate.go's PreviousTracker for the
// panel-side half of this split.
//
// Every parseX function below is a pure function of raw file content, kept
// separate from the readX function that sources that content from the
// real filesystem - so sample_test.go can exercise the actual parsing
// logic against fixed fixture text without needing a real /proc.
package hostmetrics

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Sample's JSON tags double as the wire shape of the node's push payload
// (see cmd/node/main.go's push loop) - the same struct is built locally by
// the panel's own self-sample loop and decoded from a node's POST body, so
// one type serves both without a parallel DTO to keep in sync.
type Sample struct {
	CollectedAt time.Time `json:"collected_at"`

	UptimeSeconds float64 `json:"uptime_seconds"`
	Load1m        float64 `json:"load_1m"`

	CPUTotalJiffies uint64 `json:"cpu_total_jiffies"`
	CPUIdleJiffies  uint64 `json:"cpu_idle_jiffies"`
	CPUCores        int    `json:"cpu_cores"`

	MemTotalBytes     int64 `json:"mem_total_bytes"`
	MemAvailableBytes int64 `json:"mem_available_bytes"`

	DiskTotalBytes int64 `json:"disk_total_bytes"`
	DiskUsedBytes  int64 `json:"disk_used_bytes"`

	// RxBytes/TxBytes are summed across every interface that isn't
	// loopback/docker/bridge/veth/a WireGuard tunnel's own vti-style alias
	// (see skipInterfacePrefixes) - cumulative since boot, same convention
	// as /proc/net/dev itself.
	RxBytes int64 `json:"rx_bytes"`
	TxBytes int64 `json:"tx_bytes"`

	ConnectionsEstablished int `json:"connections_established"`

	// ClientConnections is the node's own count of open CLIENT connections (its
	// presence total), which is what a node's capacity is measured against;
	// ConnectionsEstablished counts every ESTABLISHED socket on the host, the
	// node's upstream connections included. Only the node agent fills it in.
	// A node that predates the field sends nothing, which a plain int cannot
	// tell from "zero clients": see ClientConnectionsKnown.
	ClientConnections int `json:"client_connections"`

	// clientConnsSent is set by UnmarshalJSON when the decoded object carried
	// client_connections at all.
	clientConnsSent bool

	Tunnels []Tunnel `json:"tunnels,omitempty"`

	XrayRunning bool   `json:"xray_running"`
	XrayVersion string `json:"xray_version,omitempty"`
}

// ClientConnectionsKnown reports whether ClientConnections is a reading rather
// than the zero value of a field nobody filled in: it was present in the JSON
// this Sample was decoded from, or it is not zero.
func (s Sample) ClientConnectionsKnown() bool {
	return s.clientConnsSent || s.ClientConnections != 0
}

// UnmarshalJSON decodes a Sample and remembers whether client_connections was
// present, so the panel can store NULL (unknown) for a node that does not send
// it instead of a misleading 0.
func (s *Sample) UnmarshalJSON(b []byte) error {
	type plain Sample // no methods, so this does not recurse
	aux := struct {
		*plain
		// Shadows plain's field of the same name so its presence is visible.
		ClientConnections *int `json:"client_connections"`
	}{plain: (*plain)(s)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	s.clientConnsSent = aux.ClientConnections != nil
	s.ClientConnections = 0
	if aux.ClientConnections != nil {
		s.ClientConnections = *aux.ClientConnections
	}
	return nil
}

// Tunnel is one WireGuard exit. Up is a health verdict, not a link flag: a
// WireGuard device's operstate is always "unknown", so it says nothing about
// whether traffic actually gets through. When an active probe is available
// (see ApplyTunnelHealth) Up reflects that; Present says whether the network
// interface exists right now - false means the tunnel is configured on the
// host (an /etc/wireguard/<name>.conf exists) but has been brought down.
type Tunnel struct {
	Name           string   `json:"name"`
	Up             bool     `json:"up"`
	Present        bool     `json:"present"`
	RxBytes        int64    `json:"rx_bytes"`
	TxBytes        int64    `json:"tx_bytes"`
	Peers          []Peer   `json:"peers,omitempty"`
	ProbeMs        *float64 `json:"probe_ms,omitempty"`
	Error          string   `json:"error,omitempty"`
	FallbackActive bool     `json:"fallback_active,omitempty"`
}

type Peer struct {
	Endpoint                string   `json:"endpoint,omitempty"`
	LastHandshakeAgeSeconds *float64 `json:"last_handshake_age_seconds,omitempty"`
	RxBytes                 int64    `json:"rx_bytes"`
	TxBytes                 int64    `json:"tx_bytes"`
}

// skipInterfacePrefixes mirrors metrics.py's _SKIP_PREFIXES: interfaces
// that say nothing about the service and would only add noise to a total.
var skipInterfacePrefixes = []string{"lo", "docker", "br-", "veth", "vti"}

// diskPath is where the node agent's own data lives (a bind mount, so
// statfs on it reports the host's real filesystem, not a container
// overlay) - falls back to "/" if that path doesn't exist (e.g. the panel
// process, which has no such directory).
const diskPath = "/var/lib/rapido-node"

// Collect takes one sample. xrayRunning/xrayVersion are supplied by the
// caller (the node agent knows its own core's state directly; the panel
// has none, so it always passes false/"").
func Collect(xrayRunning bool, xrayVersion string) Sample {
	s := Sample{
		CollectedAt: time.Now().UTC(),
		XrayRunning: xrayRunning,
		XrayVersion: xrayVersion,
	}
	s.UptimeSeconds = parseUptime(mustReadFile("/proc/uptime"))
	s.Load1m = parseLoad1m(mustReadFile("/proc/loadavg"))
	s.CPUTotalJiffies, s.CPUIdleJiffies, s.CPUCores = parseCPUTimes(mustReadFile("/proc/stat"))
	s.MemTotalBytes, s.MemAvailableBytes = parseMemory(mustReadFile("/proc/meminfo"))
	s.DiskTotalBytes, s.DiskUsedBytes = readDisk()

	ifaces := readInterfaces()
	for name, iface := range ifaces {
		if hasSkipPrefix(name) {
			continue
		}
		s.RxBytes += iface.rxBytes
		s.TxBytes += iface.txBytes
	}
	s.ConnectionsEstablished = parseTCPEstablished(mustReadFile("/proc/net/tcp")) +
		parseTCPEstablished(mustReadFile("/proc/net/tcp6"))
	s.Tunnels = readTunnels(ifaces)
	return s
}

func hasSkipPrefix(name string) bool {
	for _, p := range skipInterfacePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// mustReadFile returns "" on any read error (missing file, permission,
// wrong platform) rather than propagating it - every parseX function below
// already treats an empty string as "nothing to report", so a plain
// degraded zero-value sample is the natural behavior when /proc isn't
// there at all (e.g. this binary running on a non-Linux dev machine).
func mustReadFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func parseUptime(raw string) float64 {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return v
}

func parseLoad1m(raw string) float64 {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return v
}

// parseCPUTimes mirrors metrics.py's cpu_times(): idle = idle-jiffies +
// iowait-jiffies (the 4th and 5th numeric fields on the "cpu " line, if
// present), total = sum of every field on that line. cores is the count
// of per-core "cpu0", "cpu1", ... lines (the bare "cpu " aggregate line
// doesn't count), falling back to 1 if none are found.
func parseCPUTimes(raw string) (total, idle uint64, cores int) {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	haveAggregate := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "cpu") && len(line) > 3 && line[3] >= '0' && line[3] <= '9' {
			cores++
			continue
		}
		if haveAggregate || !strings.HasPrefix(line, "cpu ") {
			continue
		}
		haveAggregate = true
		fields := strings.Fields(line)[1:]
		values := make([]uint64, 0, len(fields))
		for _, f := range fields {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				continue
			}
			values = append(values, v)
			total += v
		}
		if len(values) > 3 {
			idle = values[3]
			if len(values) > 4 {
				idle += values[4]
			}
		}
	}
	if cores == 0 {
		cores = 1
	}
	return total, idle, cores
}

func parseMemory(raw string) (total, available int64) {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		key, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		v *= 1024 // kB -> bytes
		switch key {
		case "MemTotal":
			total = v
		case "MemAvailable":
			available = v
		}
	}
	return total, available
}

type ifaceStats struct {
	rxBytes, txBytes int64
	operstate        string
	wireguard        bool
}

// parseInterfaceCounters parses /proc/net/dev's own cumulative rx/tx
// bytes per interface - the part of readInterfaces that has no /sys
// dependency, so it's the part sample_test.go can exercise directly.
func parseInterfaceCounters(raw string) map[string]struct{ rx, tx int64 } {
	out := make(map[string]struct{ rx, tx int64 })
	lines := strings.Split(raw, "\n")
	if len(lines) <= 2 {
		return out
	}
	for _, line := range lines[2:] {
		name, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 16 {
			continue
		}
		rx, _ := strconv.ParseInt(fields[0], 10, 64)
		tx, _ := strconv.ParseInt(fields[8], 10, 64)
		out[name] = struct{ rx, tx int64 }{rx, tx}
	}
	return out
}

// parseUeventDevType returns the DEVTYPE line of a /sys/class/net/<if>/uevent
// file. That is the only place the kernel says an interface is WireGuard:
// there is no /sys/class/net/<if>/wireguard directory, which is what this
// package used to look for - so it never found a single tunnel.
func parseUeventDevType(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "DEVTYPE="); ok {
			return v
		}
	}
	return ""
}

func isWireGuardInterface(name string) bool {
	return parseUeventDevType(mustReadFile(filepath.Join("/sys/class/net", name, "uevent"))) == "wireguard"
}

func readInterfaces() map[string]ifaceStats {
	out := make(map[string]ifaceStats)
	for name, c := range parseInterfaceCounters(mustReadFile("/proc/net/dev")) {
		operstate := mustReadFile(filepath.Join("/sys/class/net", name, "operstate"))
		out[name] = ifaceStats{
			rxBytes: c.rx, txBytes: c.tx,
			operstate: strings.TrimSpace(operstate),
			wireguard: isWireGuardInterface(name),
		}
	}
	return out
}

// wireguardConfDir is where wg-quick keeps tunnel configs. A variable so
// tests can point it at a temp directory.
var wireguardConfDir = "/etc/wireguard"

// ConfiguredTunnelNames lists the tunnels this host is set up to have -
// one /etc/wireguard/<name>.conf each. Comparing it with the interfaces that
// exist right now is what tells a tunnel that was switched off apart from a
// tunnel that never belonged on this server.
func ConfiguredTunnelNames() []string {
	entries, err := os.ReadDir(wireguardConfDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".conf"); ok && name != "" && !e.IsDir() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// TunnelNames is every WireGuard tunnel worth watching on this host:
// configured ones plus any WireGuard interface that exists right now.
func TunnelNames() []string {
	seen := make(map[string]bool)
	var names []string
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, n := range ConfiguredTunnelNames() {
		add(n)
	}
	for n, iface := range readInterfaces() {
		if iface.wireguard {
			add(n)
		}
	}
	sort.Strings(names)
	return names
}

// TunnelHealth is what an active probe learned about one tunnel. Supplied
// by the caller (the node agent), never measured here - Collect stays a
// pure reading of /proc and /sys.
type TunnelHealth struct {
	Present        bool
	Up             bool
	ProbeMs        *float64
	Error          string
	FallbackActive bool
}

// ApplyTunnelHealth turns the raw interface reading into the verdict the
// panel shows. configured lists tunnels the host is set up for: one whose
// interface is gone is reported as down and not present rather than silently
// disappearing - which is exactly what a switched-off tunnel looks like.
// A tunnel with no probe result keeps its link-state guess. Tunnels are
// returned sorted by name.
func ApplyTunnelHealth(tunnels []Tunnel, configured []string, health map[string]TunnelHealth) []Tunnel {
	byName := make(map[string]*Tunnel, len(tunnels)+len(configured))
	for i := range tunnels {
		t := tunnels[i]
		t.Present = true
		byName[t.Name] = &t
	}
	for _, name := range configured {
		if _, ok := byName[name]; !ok {
			byName[name] = &Tunnel{Name: name, Up: false, Present: false, Error: "interface not found"}
		}
	}
	for name, h := range health {
		t, ok := byName[name]
		if !ok {
			continue
		}
		t.Present = h.Present
		t.Up = h.Up
		t.ProbeMs = h.ProbeMs
		t.Error = h.Error
		t.FallbackActive = h.FallbackActive
	}
	out := make([]Tunnel, 0, len(byName))
	for _, t := range byName {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// parseTCPEstablished counts ESTABLISHED (state 0x01) sockets in a
// /proc/net/tcp or /proc/net/tcp6-shaped listing.
func parseTCPEstablished(raw string) int {
	lines := strings.Split(raw, "\n")
	if len(lines) <= 1 {
		return 0
	}
	count := 0
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		state, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil {
			continue
		}
		if state == 1 { // TCP_ESTABLISHED
			count++
		}
	}
	return count
}

// parseWireGuardDump fills in peer data (from `wg show all dump`'s own
// tab-separated output) onto whichever tunnels are already present in
// byName - interfaces `wg` mentions that aren't already known WireGuard
// interfaces (i.e. not in byName) are ignored, mirroring metrics.py's own
// wireguard()'s behavior of only ever trusting /sys for tunnel identity.
func parseWireGuardDump(raw string, byName map[string]*Tunnel, now time.Time) {
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 9 {
			continue
		}
		iface := fields[0]
		t, ok := byName[iface]
		if !ok {
			continue
		}
		endpoint := fields[3]
		if endpoint == "(none)" {
			endpoint = ""
		}
		var handshakeAge *float64
		if hs, err := strconv.ParseInt(fields[5], 10, 64); err == nil && hs > 0 {
			age := now.Sub(time.Unix(hs, 0)).Seconds()
			handshakeAge = &age
		}
		rx, _ := strconv.ParseInt(fields[6], 10, 64)
		tx, _ := strconv.ParseInt(fields[7], 10, 64)
		t.Peers = append(t.Peers, Peer{
			Endpoint: endpoint, LastHandshakeAgeSeconds: handshakeAge, RxBytes: rx, TxBytes: tx,
		})
	}
}

// readTunnels mirrors metrics.py's wireguard(): interface identity/byte
// counters/up-state come straight from /sys and /proc (always available);
// per-peer handshake age needs the `wg` CLI, so it's populated only when
// that succeeds - a missing/failing `wg` degrades to tunnel-level data
// with no peers, not a hard error.
func readTunnels(ifaces map[string]ifaceStats) []Tunnel {
	var tunnels []Tunnel
	for name, iface := range ifaces {
		if !iface.wireguard {
			continue
		}
		tunnels = append(tunnels, Tunnel{
			Name:    name,
			Up:      iface.operstate == "up" || iface.operstate == "unknown",
			Present: true,
			RxBytes: iface.rxBytes,
			TxBytes: iface.txBytes,
		})
	}
	if len(tunnels) == 0 {
		return tunnels
	}
	byName := make(map[string]*Tunnel, len(tunnels))
	for i := range tunnels {
		byName[tunnels[i].Name] = &tunnels[i]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "wg", "show", "all", "dump").Output()
	if err != nil {
		return tunnels
	}
	parseWireGuardDump(string(out), byName, time.Now())
	return tunnels
}

package hostmetrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const uptimeFixture = "1234567.89 987654.32\n"

func TestParseUptime(t *testing.T) {
	if got := parseUptime(uptimeFixture); got != 1234567.89 {
		t.Errorf("parseUptime = %v, want 1234567.89", got)
	}
	if got := parseUptime(""); got != 0 {
		t.Errorf("parseUptime(empty) = %v, want 0", got)
	}
}

const loadavgFixture = "0.52 0.38 0.21 3/456 78901\n"

func TestParseLoad1m(t *testing.T) {
	if got := parseLoad1m(loadavgFixture); got != 0.52 {
		t.Errorf("parseLoad1m = %v, want 0.52", got)
	}
}

// A realistic 2-core /proc/stat sample - the aggregate "cpu " line's
// fields are user/nice/system/idle/iowait/irq/softirq/steal.
const statFixture = `cpu  1000 10 200 8000 50 0 0 0
cpu0 500 5 100 4000 25 0 0 0
cpu1 500 5 100 4000 25 0 0 0
intr 12345 0 0 0
ctxt 98765
btime 1690000000
processes 4321
`

func TestParseCPUTimes(t *testing.T) {
	total, idle, cores := parseCPUTimes(statFixture)
	wantTotal := uint64(1000 + 10 + 200 + 8000 + 50)
	wantIdle := uint64(8000 + 50) // idle + iowait
	if total != wantTotal {
		t.Errorf("total = %d, want %d", total, wantTotal)
	}
	if idle != wantIdle {
		t.Errorf("idle = %d, want %d", idle, wantIdle)
	}
	if cores != 2 {
		t.Errorf("cores = %d, want 2", cores)
	}
}

func TestParseCPUTimesEmptyDefaultsToOneCore(t *testing.T) {
	_, _, cores := parseCPUTimes("")
	if cores != 1 {
		t.Errorf("cores on empty input = %d, want 1 (never zero - callers divide by this)", cores)
	}
}

const meminfoFixture = `MemTotal:       16384000 kB
MemFree:         2048000 kB
MemAvailable:    9000000 kB
SwapTotal:              0 kB
SwapFree:               0 kB
`

func TestParseMemory(t *testing.T) {
	total, available := parseMemory(meminfoFixture)
	if total != 16384000*1024 {
		t.Errorf("total = %d, want %d", total, 16384000*1024)
	}
	if available != 9000000*1024 {
		t.Errorf("available = %d, want %d", available, 9000000*1024)
	}
}

// A realistic /proc/net/dev sample: 2 header lines, then one loopback
// (must be skipped by the caller via skipInterfacePrefixes, not by this
// parser itself - parseInterfaceCounters returns every named interface it
// finds), one real NIC, and one WireGuard-shaped interface name (identity
// as "wireguard" itself is a /sys fact this parser doesn't have, by
// design - see readInterfaces).
const netDevFixture = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:  123456     100    0    0    0     0          0         0   123456     100    0    0    0     0       0          0
  eth0: 5000000    3000    0    0    0     0          0         0  2000000    1500    0    0    0     0       0          0
  wg0:   700000     500    0    0    0     0          0         0   300000     400    0    0    0     0       0          0
`

func TestParseInterfaceCounters(t *testing.T) {
	got := parseInterfaceCounters(netDevFixture)
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3: %+v", len(got), got)
	}
	if c := got["eth0"]; c.rx != 5000000 || c.tx != 2000000 {
		t.Errorf("eth0 = %+v, want {5000000 2000000}", c)
	}
	if c := got["wg0"]; c.rx != 700000 || c.tx != 300000 {
		t.Errorf("wg0 = %+v, want {700000 300000}", c)
	}
}

func TestParseInterfaceCountersMalformedIsIgnored(t *testing.T) {
	got := parseInterfaceCounters("header1\nheader2\nnot-a-real-line-at-all\n")
	if len(got) != 0 {
		t.Errorf("got = %+v, want empty", got)
	}
}

// /proc/net/tcp's own column layout: sl, local_address, rem_address, st, ...
// st is hex; 01 = ESTABLISHED, 0A = LISTEN.
const tcpFixture = ` sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
   1: 0100007F:C350 0100007F:9C40 01 00000000:00000000 00:00000000 00000000     0        0 12346 1 0000000000000000 100 0 0 10 0
   2: 0100007F:9C41 0100007F:C351 01 00000000:00000000 00:00000000 00000000     0        0 12347 1 0000000000000000 100 0 0 10 0
`

func TestParseTCPEstablished(t *testing.T) {
	if got := parseTCPEstablished(tcpFixture); got != 2 {
		t.Errorf("parseTCPEstablished = %d, want 2 (one LISTEN, two ESTABLISHED)", got)
	}
}

func TestParseTCPEstablishedHeaderOnly(t *testing.T) {
	if got := parseTCPEstablished(" sl  local_address rem_address   st\n"); got != 0 {
		t.Errorf("parseTCPEstablished(header only) = %d, want 0", got)
	}
}

func TestParseWireGuardDump(t *testing.T) {
	now := time.Unix(1700000300, 0)
	tunnels := map[string]*Tunnel{"wg0": {Name: "wg0"}}
	dump := "wg0\tprivkey\tpubkey\t203.0.113.5:51820\t10.0.0.2/32\t1700000000\t123456\t654321\t25\n" +
		"wg0\tprivkey\tpubkey2\t(none)\t10.0.0.3/32\t0\t0\t0\t25\n" +
		"wg1-not-a-known-tunnel\tprivkey\tpubkey\t203.0.113.9:51820\t10.0.1.2/32\t1700000000\t1\t1\t25\n"

	parseWireGuardDump(dump, tunnels, now)

	wg0 := tunnels["wg0"]
	if len(wg0.Peers) != 2 {
		t.Fatalf("len(wg0.Peers) = %d, want 2", len(wg0.Peers))
	}
	p0 := wg0.Peers[0]
	if p0.Endpoint != "203.0.113.5:51820" {
		t.Errorf("peer0 endpoint = %q", p0.Endpoint)
	}
	if p0.LastHandshakeAgeSeconds == nil || *p0.LastHandshakeAgeSeconds != 300 {
		t.Errorf("peer0 handshake age = %v, want 300s", p0.LastHandshakeAgeSeconds)
	}
	if p0.RxBytes != 123456 || p0.TxBytes != 654321 {
		t.Errorf("peer0 bytes = %d/%d, want 123456/654321", p0.RxBytes, p0.TxBytes)
	}

	p1 := wg0.Peers[1]
	if p1.Endpoint != "" {
		t.Errorf("peer1 endpoint = %q, want empty for (none)", p1.Endpoint)
	}
	if p1.LastHandshakeAgeSeconds != nil {
		t.Errorf("peer1 handshake age = %v, want nil (handshake=0 means never)", p1.LastHandshakeAgeSeconds)
	}

	if len(tunnels) != 1 {
		t.Errorf("an interface not already in byName must not be added: %+v", tunnels)
	}
}

func TestCollectDoesNotPanicOnAMissingProc(t *testing.T) {
	// On this dev machine (or any non-Linux GOOS), every /proc read fails -
	// Collect must degrade to a mostly-zero Sample, never panic.
	s := Collect(true, "test-version")
	if !s.XrayRunning || s.XrayVersion != "test-version" {
		t.Errorf("caller-supplied fields not preserved: %+v", s)
	}
}

// A WireGuard device's uevent is the only place the kernel names its type.
// This exact text was read from a real tunnel on a production node - there
// is no /sys/class/net/<if>/wireguard directory.
func TestParseUeventDevTypeRecognisesWireGuard(t *testing.T) {
	if got := parseUeventDevType("DEVTYPE=wireguard\nINTERFACE=uk\nIFINDEX=14\n"); got != "wireguard" {
		t.Errorf("parseUeventDevType = %q, want wireguard", got)
	}
	if got := parseUeventDevType("INTERFACE=eth0\nIFINDEX=2\n"); got != "" {
		t.Errorf("an ordinary NIC has no DEVTYPE, got %q", got)
	}
	if got := parseUeventDevType(""); got != "" {
		t.Errorf("empty uevent = %q, want empty", got)
	}
}

func TestConfiguredTunnelNamesReadsWgQuickConfigs(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"uk.conf", "Czech.conf", "notes.txt", "sweden.conf.bak"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.conf"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := wireguardConfDir
	wireguardConfDir = dir
	t.Cleanup(func() { wireguardConfDir = old })

	got := ConfiguredTunnelNames()
	want := []string{"Czech", "uk"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ConfiguredTunnelNames = %v, want %v", got, want)
	}

	wireguardConfDir = filepath.Join(dir, "does-not-exist")
	if got := ConfiguredTunnelNames(); got != nil {
		t.Errorf("a host without /etc/wireguard has no configured tunnels, got %v", got)
	}
}

func TestApplyTunnelHealthReportsASwitchedOffTunnelAsDown(t *testing.T) {
	probeMs := 41.5
	tunnels := []Tunnel{
		{Name: "uk", Up: true, RxBytes: 10},
		{Name: "sweden", Up: true, RxBytes: 20},
	}
	configured := []string{"italy", "sweden", "uk"}
	health := map[string]TunnelHealth{
		"uk":     {Present: true, Up: true, ProbeMs: &probeMs},
		"sweden": {Present: true, Up: false, Error: "probe timed out", FallbackActive: true},
	}

	got := ApplyTunnelHealth(tunnels, configured, health)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (italy is configured but its interface is gone): %+v", len(got), got)
	}
	byName := map[string]Tunnel{}
	for _, tn := range got {
		byName[tn.Name] = tn
	}
	if got[0].Name != "italy" || got[1].Name != "sweden" || got[2].Name != "uk" {
		t.Errorf("not sorted by name: %+v", got)
	}
	if it := byName["italy"]; it.Up || it.Present || it.Error == "" {
		t.Errorf("italy = %+v, want down, not present, with an explanation", it)
	}
	if uk := byName["uk"]; !uk.Up || !uk.Present || uk.ProbeMs == nil || *uk.ProbeMs != 41.5 || uk.RxBytes != 10 {
		t.Errorf("uk = %+v, want up/present with the probe latency and the original byte counters", uk)
	}
	if sw := byName["sweden"]; sw.Up || !sw.Present || !sw.FallbackActive || sw.Error != "probe timed out" {
		t.Errorf("sweden = %+v, want present but down, fallback active", sw)
	}
}

func TestApplyTunnelHealthWithoutProbeKeepsLinkStateGuess(t *testing.T) {
	got := ApplyTunnelHealth([]Tunnel{{Name: "uk", Up: true}}, nil, nil)
	if len(got) != 1 || !got[0].Up || !got[0].Present {
		t.Errorf("got %+v, want the tunnel untouched but marked present", got)
	}
	// Health for a name that is neither read nor configured is ignored, not invented.
	got = ApplyTunnelHealth(nil, nil, map[string]TunnelHealth{"ghost": {Up: true}})
	if len(got) != 0 {
		t.Errorf("a tunnel that exists nowhere must not be invented: %+v", got)
	}
}

func TestSampleClientConnectionsRoundTripAndPresence(t *testing.T) {
	// A node that sends the field: known, even when it is an honest zero.
	var zero Sample
	if err := json.Unmarshal([]byte(`{"connections_established":9,"client_connections":0}`), &zero); err != nil {
		t.Fatal(err)
	}
	if !zero.ClientConnectionsKnown() || zero.ClientConnections != 0 || zero.ConnectionsEstablished != 9 {
		t.Errorf("explicit zero: known=%v value=%d established=%d, want known 0 / 9", zero.ClientConnectionsKnown(), zero.ClientConnections, zero.ConnectionsEstablished)
	}
	var some Sample
	if err := json.Unmarshal([]byte(`{"client_connections":4300}`), &some); err != nil {
		t.Fatal(err)
	}
	if !some.ClientConnectionsKnown() || some.ClientConnections != 4300 {
		t.Errorf("known=%v value=%d, want known 4300", some.ClientConnectionsKnown(), some.ClientConnections)
	}

	// A node that predates the field sends nothing: unknown, not zero.
	var old Sample
	if err := json.Unmarshal([]byte(`{"connections_established":9,"cpu_cores":4}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.ClientConnectionsKnown() || old.ClientConnections != 0 || old.CPUCores != 4 {
		t.Errorf("absent field: known=%v value=%d cores=%d, want unknown 0 / 4", old.ClientConnectionsKnown(), old.ClientConnections, old.CPUCores)
	}
	var null Sample
	if err := json.Unmarshal([]byte(`null`), &null); err != nil || null.ClientConnectionsKnown() {
		t.Errorf("null host: err=%v known=%v, want no error and unknown", err, null.ClientConnectionsKnown())
	}

	// A locally built sample counts as known once it carries a reading, and the
	// field always goes out on the wire.
	local := Sample{ClientConnections: 12}
	if !local.ClientConnectionsKnown() {
		t.Error("a non-zero local reading must be known")
	}
	if (Sample{}).ClientConnectionsKnown() {
		t.Error("the zero Sample (the panel's own, before it is filled) must be unknown")
	}
	raw, err := json.Marshal(Sample{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["client_connections"]) != "0" {
		t.Errorf("marshalled client_connections = %s, want 0 (always present)", fields["client_connections"])
	}

	// The type keeps decoding the rest of the sample: tunnels and timestamps survive the custom decoder.
	var full Sample
	in := `{"collected_at":"2026-09-25T04:00:00Z","load_1m":1.5,"tunnels":[{"name":"uk","up":true,"present":true}],"client_connections":7}`
	if err := json.Unmarshal([]byte(in), &full); err != nil {
		t.Fatal(err)
	}
	if full.Load1m != 1.5 || len(full.Tunnels) != 1 || full.Tunnels[0].Name != "uk" || full.CollectedAt.Year() != 2026 || full.ClientConnections != 7 {
		t.Errorf("decoded sample = %+v", full)
	}
	if err := json.Unmarshal([]byte(`{"client_connections":"many"}`), &full); err == nil {
		t.Error("a non-numeric client_connections must be a decode error")
	}
}

func TestClientConnsColumn(t *testing.T) {
	if c := clientConnsColumn(Sample{}); c.Valid {
		t.Errorf("unknown must be NULL, got %+v", c)
	}
	if c := clientConnsColumn(Sample{ClientConnections: 250}); !c.Valid || c.Int32 != 250 {
		t.Errorf("reading = %+v, want 250", c)
	}
	var sent Sample
	if err := json.Unmarshal([]byte(`{"client_connections":0}`), &sent); err != nil {
		t.Fatal(err)
	}
	if c := clientConnsColumn(sent); !c.Valid || c.Int32 != 0 {
		t.Errorf("an explicit 0 is a reading, got %+v", c)
	}
	if c := clientConnsColumn(Sample{ClientConnections: -5}); !c.Valid || c.Int32 != 0 {
		t.Errorf("negative must clamp to 0, got %+v", c)
	}
}

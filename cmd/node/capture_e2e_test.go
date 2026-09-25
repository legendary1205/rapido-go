package main

import (
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/legendary1205/rapido-go/internal/nodelog"
)

// coreNoise drives the misbehaving clients a public proxy port meets all day and
// returns nothing: it only makes the core log about them.
func coreNoise(t *testing.T, f protoFixture, port uint16, echoHost string, echoPort int) {
	t.Helper()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))

	// A connection that says nothing and leaves.
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
	}
	// A scanner that speaks something else.
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
		time.Sleep(20 * time.Millisecond)
		c.Close()
	}
	// A client with credentials the node does not know.
	stranger := f.user("stranger", 77)
	if conn, err := f.dialProxied(port, stranger, echoHost, echoPort); err == nil {
		conn.Write([]byte("hello"))
		time.Sleep(20 * time.Millisecond)
		conn.Close()
	}
}

// The wiring proven against the real core: its log reaches the journal only
// through the capture, without colour codes, benign client errors are dropped
// and counted (not one of them reaches the journal), and everything else - the
// core's own start-up lines included - passes through into both the journal and
// the ring. A core restart keeps writing into the same capture.
func TestCoreLogGoesThroughTheCapture(t *testing.T) {
	for _, f := range protoFixtures() {
		t.Run(f.name, func(t *testing.T) {
			journalFile, err := os.CreateTemp(t.TempDir(), "journal")
			if err != nil {
				t.Fatal(err)
			}
			defer journalFile.Close()
			realStderr := os.Stderr
			os.Stderr = journalFile
			defer func() { os.Stderr = realStderr }()

			ring := nodelog.NewRing(nodelog.DefaultRingSize)
			agg := nodelog.NewAggregator()
			capture, err := nodelog.InterceptStderr(ring, agg)
			if err != nil {
				t.Fatal(err)
			}
			defer capture.Close()

			echoHost, echoPort := startEcho(t)
			port := freeTCPPort(t)
			alice := f.user("alice", 1)
			srv := newTestServer(t)
			runCore := func(level string) {
				srv.mu.Lock()
				defer srv.mu.Unlock()
				req := startRequest{
					Inbounds: []inboundSpec{f.inbound("main", []uint16{port}, alice)},
					Core:     &coreSpec{LogLevel: level},
				}
				if err := srv.startNodeLocked(req); err != nil {
					t.Fatalf("start: %v", err)
				}
			}

			// Two cores in a row, as a config change makes them.
			runCore("trace")
			coreNoise(t, f, port, echoHost, echoPort)
			if err := f.echoProxied(port, alice, echoHost, echoPort); err != nil {
				t.Fatalf("a real user through the node: %v", err)
			}
			srv.mu.Lock()
			srv.stopNodeLocked()
			srv.mu.Unlock()
			runCore("trace")
			coreNoise(t, f, port, echoHost, echoPort)
			time.Sleep(200 * time.Millisecond)
			srv.mu.Lock()
			srv.stopNodeLocked()
			srv.mu.Unlock()

			capture.Close() // drains the pipe
			raw, err := os.ReadFile(journalFile.Name())
			if err != nil {
				t.Fatal(err)
			}
			journal := string(raw)

			if strings.Contains(journal, "\x1b") {
				t.Errorf("the journal has colour codes: the core was not told to switch them off:\n%q", firstLines(journal, 5))
			}
			if !strings.Contains(journal, "sing-box started") || !strings.Contains(journal, "inbound/"+f.protocol+"[main]") {
				t.Errorf("the core's ordinary lines did not reach the journal:\n%s", firstLines(journal, 8))
			}
			totals := agg.Totals()
			if len(totals) == 0 {
				t.Fatalf("no noise was recognised at all; the journal holds:\n%s", journal)
			}
			t.Logf("dropped: %v", totals)
			for _, line := range strings.Split(journal, "\n") {
				if strings.HasPrefix(line, "ERROR") || strings.HasPrefix(line, "WARN") {
					t.Logf("kept: %s", line)
				}
			}
			for _, line := range strings.Split(journal, "\n") {
				level, text, ok := nodelog.ParseLine(line)
				if !ok {
					continue
				}
				if kind, benign := nodelog.Classify(level, text); benign {
					t.Errorf("a benign line (%s) reached the journal: %s", kind, line)
				}
			}
			var sum int64
			for _, n := range totals {
				sum += n
			}
			if sum < 2 {
				t.Errorf("only %d lines dropped for four misbehaving clients across two cores", sum)
			}
			if ring.Len() == 0 {
				t.Fatal("the ring is empty")
			}
			for _, e := range ring.Since(0, nodelog.DefaultRingSize) {
				if strings.Contains(e.Text, "\x1b") {
					t.Fatalf("ring entry with colour codes: %q", e.Text)
				}
				if kind, benign := nodelog.Classify(e.Level, e.Text); benign {
					t.Errorf("a benign line (%s) was kept in the ring: %s", kind, e.Text)
				}
			}
		})
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func TestCoreLogOptionsAreAlwaysColourFree(t *testing.T) {
	for name, core := range map[string]*coreSpec{
		"no core section": nil,
		"no level":        {},
		"warning":         {LogLevel: "warning"},
		"debug":           {LogLevel: "debug"},
	} {
		_, _, _, log, _, err := buildCoreOptions(core, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if log == nil || !log.DisableColor {
			t.Errorf("%s: log options = %+v, want colour disabled", name, log)
			continue
		}
		want := ""
		if core != nil {
			want = core.LogLevel
		}
		if log.Level != want {
			t.Errorf("%s: level = %q, want the configured %q", name, log.Level, want)
		}
	}
}

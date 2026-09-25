package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
)

// pushedHost runs one pushOnce against a fake panel and returns the "host"
// object of the node-report it received.
func pushedHost(t *testing.T, s *server) map[string]json.RawMessage {
	t.Helper()
	var (
		mu   sync.Mutex
		body []byte
	)
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/api/internal/node-report" {
			mu.Lock()
			body = b
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer panel.Close()

	s.pushOnce(context.Background(), &http.Client{Timeout: 5 * time.Second}, config{PanelURL: panel.URL, ReportSecret: testSecret})

	mu.Lock()
	defer mu.Unlock()
	if body == nil {
		t.Fatal("the panel got no node-report")
	}
	var report struct {
		Host map[string]json.RawMessage `json:"host"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatalf("decode report: %v: %s", err, body)
	}
	return report.Host
}

func TestPushReportCarriesTheClientConnectionTotal(t *testing.T) {
	traffc := traffic.NewManager()
	s := &server{logger: quietLogger(), traffic: traffc}

	// Nothing open: the field is still sent, as an explicit 0 (a reading, not
	// "unknown", which is what leaving it out means).
	if got := string(pushedHost(t, s)["client_connections"]); got != "0" {
		t.Errorf("idle node client_connections = %q, want 0", got)
	}

	end1 := traffc.OpenConn("alice", 20000)
	end2 := traffc.OpenConn("alice", 20001)
	end3 := traffc.OpenConn("bob", 20000)
	defer end1()
	defer end2()
	defer end3()
	if got := string(pushedHost(t, s)["client_connections"]); got != "3" {
		t.Errorf("client_connections = %q, want 3 (the same total node-live sends)", got)
	}
	end3()
	if got := string(pushedHost(t, s)["client_connections"]); got != "2" {
		t.Errorf("client_connections after a close = %q, want 2", got)
	}
}

func TestClientConnectionsIsZeroWithoutATrafficManager(t *testing.T) {
	if got := (&server{logger: quietLogger()}).clientConnections(); got != 0 {
		t.Errorf("clientConnections = %d, want 0 when nothing is counting", got)
	}
}

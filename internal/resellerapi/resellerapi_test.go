package resellerapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReportUsagesPostsEachAdminsUsageToTheUsagesEndpoint(t *testing.T) {
	var gotPath, gotContentType string
	var gotBody []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotContentType = r.Method+" "+r.URL.Path, r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	client := NewClient(&http.Client{Timeout: 5 * time.Second})
	err := client.ReportUsages(context.Background(), Config{Secret: "s3cret", URL: srv.URL}, []Usage{
		{Username: "reseller_a", Usage: 1073741824}, {Username: "reseller_b", Usage: 5},
	})
	if err != nil {
		t.Fatalf("ReportUsages: %v", err)
	}

	if gotPath != "POST /api/subscriptions/s3cret/usages" {
		t.Errorf("request = %q", gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("content type = %q", gotContentType)
	}
	if len(gotBody) != 2 || gotBody[0]["username"] != "reseller_a" || gotBody[0]["usage"] != float64(1073741824) ||
		gotBody[1]["username"] != "reseller_b" || gotBody[1]["usage"] != float64(5) {
		t.Errorf("body = %v", gotBody)
	}
}

// A rejected report must be an error: the caller keeps the usage queued
// only when told the bot did not take it.
func TestReportUsagesReturnsAnErrorWhenTheBotRejectsTheReport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"Invalid secret"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	client := NewClient(&http.Client{Timeout: 5 * time.Second})
	if err := client.ReportUsages(context.Background(), Config{Secret: "wrong", URL: srv.URL}, []Usage{{Username: "a", Usage: 1}}); err == nil {
		t.Fatal("expected an error for HTTP 403")
	}
}

func TestCanReportUsageNeedsBothSecretAndURL(t *testing.T) {
	cases := []struct {
		cfg  Config
		want bool
	}{
		{Config{}, false},
		{Config{Secret: "s"}, false},
		{Config{URL: "http://bot"}, false},
		{Config{Secret: "s", URL: "http://bot"}, true},
	}
	for _, tc := range cases {
		if got := tc.cfg.CanReportUsage(); got != tc.want {
			t.Errorf("%+v: CanReportUsage = %v, want %v", tc.cfg, got, tc.want)
		}
	}

	client := NewClient(http.DefaultClient)
	if err := client.ReportUsages(context.Background(), Config{Secret: "s"}, []Usage{{Username: "a", Usage: 1}}); err == nil {
		t.Error("a report with no URL must fail instead of pretending it was delivered")
	}
}

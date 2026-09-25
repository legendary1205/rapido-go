package telegrambot

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/legendary1205/rapido-go/internal/integrationsettings"
)

// backupFake stands in for the one part of the panel that shells out to pg_dump,
// which the test machine may not have; every other route goes to the real router.
type backupFake struct {
	real     http.Handler
	name     string
	size     int64
	payload  []byte
	failWith string
	posts    atomic.Int32
	gets     atomic.Int32
}

func (b *backupFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/settings/backup":
		b.posts.Add(1)
		if b.failWith != "" {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"detail":"`+b.failWith+`"}`)
			return
		}
		io.WriteString(w, `{"filename":"`+b.name+`","size_bytes":`+itoa(int(b.size))+`,"created_at":"2026-01-01T00:00:00Z"}`)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/settings/backup/"):
		b.gets.Add(1)
		w.Write(b.payload)
	default:
		b.real.ServeHTTP(w, r)
	}
}

func TestBackupIsSudoOnlyAndSendsTheFile(t *testing.T) {
	e := newEnv(t)
	fake := &backupFake{real: e.router, name: "rapido_20260101T000000.000Z.sql.gz", payload: []byte("gzip-bytes-of-a-dump"), size: 20}
	e.handler = fake

	e.send(sudoUID, "/start")
	e.press(sudoUID, "Backup")
	mustContain(t, e.screen(), "Create a database backup")
	if fake.posts.Load() != 0 {
		t.Fatal("a backup was taken before the confirmation")
	}
	e.press(sudoUID, "Create and send")

	var doc *fakeCall
	for _, c := range e.tg.snapshot() {
		if c.Method == "sendDocument" {
			c := c
			doc = &c
		}
	}
	if doc == nil {
		t.Fatal("no document was sent")
	}
	if doc.FileName != fake.name || string(doc.File) != string(fake.payload) {
		t.Errorf("document = %q %q, want the backup file", doc.FileName, doc.File)
	}
	mustContain(t, doc.Body["caption"].(string), fake.name)
	mustContain(t, e.screen(), "Backup sent.")
	if !e.hasButtonData("h") {
		t.Error("the backup screen has no Home button")
	}
}

func TestBackupFailureAndOversizeAreReportedReadably(t *testing.T) {
	e := newEnv(t)
	fake := &backupFake{real: e.router, name: "rapido_20260101T000000.000Z.sql.gz", failWith: "backup failed: pg_dump: not found"}
	e.handler = fake
	e.tap(sudoUID, "bk:y")
	mustContain(t, e.screen(), "Backup failed: backup failed: pg_dump: not found")
	if e.tg.count("sendDocument") != 0 {
		t.Error("a failed backup still sent a document")
	}

	// A dump over Telegram's upload ceiling is never downloaded into memory.
	fake.failWith, fake.size = "", 60<<20
	e.tap(sudoUID, "bk:y")
	mustContain(t, e.screen(), "too large for Telegram")
	if fake.gets.Load() != 0 || e.tg.count("sendDocument") != 0 {
		t.Error("an oversize backup was downloaded or sent")
	}

	// Telegram refusing the upload is a clear message, not silence.
	fake.size = 20
	fake.payload = []byte("x")
	e.tg.mu.Lock()
	e.tg.failNext["sendDocument"] = 1
	e.tg.mu.Unlock()
	e.tap(sudoUID, "bk:y")
	mustContain(t, e.screen(), "could not be sent to Telegram")
}

// pollUntil waits for cond, since the poll loop runs on its own goroutine.
func pollUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRunStaysDormantWithoutATokenAndWakesWhenOneIsSet(t *testing.T) {
	e := newEnv(t)
	e.console.settingsEvery = 40 * time.Millisecond
	e.console.pollTimeoutSec = 1
	e.settings.set(func(v *integrationsettings.Values) { v.TelegramAPIToken = "" })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.console.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	// No token: not one request leaves the process.
	time.Sleep(400 * time.Millisecond)
	if n := len(e.tg.snapshot()); n != 0 {
		t.Fatalf("a dormant console made %d Bot API calls", n)
	}

	// The token is set at runtime, no restart: polling starts and serves updates.
	e.settings.set(func(v *integrationsettings.Values) { v.TelegramAPIToken = envToken })
	pollUntil(t, "polling to start", func() bool { return e.tg.count("getUpdates") > 0 })
	e.tg.updates <- tgUpdate{UpdateID: 900, Message: &tgMessage{
		MessageID: 1, From: &tgUser{ID: sudoUID, LanguageCode: "en"}, Chat: tgChat{ID: sudoUID, Type: "private"}, Text: "/start",
	}}
	pollUntil(t, "the /start reply", func() bool { return e.tg.count("sendMessage") > 0 })
	mustContain(t, e.screen(), "Signed in as")

	// The offset moves past what was handled, so it is not fetched again.
	pollUntil(t, "the offset to advance", func() bool {
		calls := e.tg.snapshot()
		last := calls[len(calls)-1]
		return last.Method == "getUpdates" && last.Body["offset"] == float64(901)
	})

	// Clearing the token stops the polling again.
	e.settings.set(func(v *integrationsettings.Values) { v.TelegramAPIToken = "" })
	time.Sleep(300 * time.Millisecond) // let an in-flight poll finish
	settled := e.tg.count("getUpdates")
	time.Sleep(400 * time.Millisecond)
	if again := e.tg.count("getUpdates"); again != settled {
		t.Errorf("polling continued after the token was removed (%d -> %d calls)", settled, again)
	}
}

func TestRunRecoversFromNetworkErrorsWithBackoff(t *testing.T) {
	e := newEnv(t)
	e.console.settingsEvery = 40 * time.Millisecond
	e.console.pollTimeoutSec = 1
	e.tg.mu.Lock()
	e.tg.failNext["getUpdates"] = 1 // one 500, then normal service
	e.tg.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.console.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	pollUntil(t, "the failed poll", func() bool { return e.tg.count("getUpdates") >= 1 })
	e.tg.updates <- tgUpdate{UpdateID: 1, Message: &tgMessage{
		MessageID: 1, From: &tgUser{ID: sudoUID, LanguageCode: "en"}, Chat: tgChat{ID: sudoUID, Type: "private"}, Text: "/start",
	}}
	// The loop waits about a second (the first backoff step) and then carries on.
	pollUntil(t, "the reply after the error", func() bool { return e.tg.count("sendMessage") > 0 })
}

func TestRunRefusesAWrongTokenQuietly(t *testing.T) {
	e := newEnv(t)
	e.console.settingsEvery = 40 * time.Millisecond
	e.console.pollTimeoutSec = 1
	e.settings.set(func(v *integrationsettings.Values) { v.TelegramAPIToken = "999:not-the-fake-servers-token" })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.console.Run(ctx); close(done) }()
	time.Sleep(500 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop after its context was cancelled")
	}
	// A rejected token is retried on a long interval, never hammered.
	if n := e.tg.unauthorized.Load(); n != 1 {
		t.Errorf("%d requests with a rejected token in half a second, want exactly 1", n)
	}
}

package telegrambot

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "123456:SECRET-token-value"

func TestBotClientNeverLeaksTheTokenInErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more: the transport error embeds the URL

	b := newBotClient(testToken, url, "")
	_, err := b.getUpdates(context.Background(), 0, 1)
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), "SECRET-token-value") || strings.Contains(err.Error(), testToken) {
		t.Fatalf("error text leaks the bot token: %v", err)
	}
}

func TestBotClientParsesAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 9","parameters":{"retry_after":9}}`)
	}))
	defer srv.Close()

	b := newBotClient(testToken, srv.URL, "")
	err := b.answerCallback(context.Background(), "cb", "")
	var ae *apiError
	if !errors.As(err, &ae) {
		t.Fatalf("want *apiError, got %v", err)
	}
	if ae.Code != 429 || ae.RetryAfter != 9 || !strings.Contains(ae.Description, "Too Many") {
		t.Errorf("apiError = %+v", ae)
	}
}

func TestBotClientMultipartUpload(t *testing.T) {
	var gotChat, gotCaption, gotName string
	var gotBytes []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendDocument") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
		}
		gotChat, gotCaption = r.FormValue("chat_id"), r.FormValue("caption")
		f, hdr, err := r.FormFile("document")
		if err != nil {
			t.Errorf("FormFile: %v", err)
		} else {
			gotBytes, _ = io.ReadAll(f)
			gotName = hdr.Filename
		}
		io.WriteString(w, `{"ok":true,"result":{"message_id":7}}`)
	}))
	defer srv.Close()

	b := newBotClient(testToken, srv.URL, "")
	if err := b.sendDocument(context.Background(), 42, "rapido.sql.gz", []byte("payload"), "cap"); err != nil {
		t.Fatalf("sendDocument: %v", err)
	}
	if gotChat != "42" || gotCaption != "cap" || gotName != "rapido.sql.gz" || string(gotBytes) != "payload" {
		t.Errorf("upload came through as chat=%q caption=%q name=%q body=%q", gotChat, gotCaption, gotName, gotBytes)
	}
}

func TestBotClientHonoursProxy(t *testing.T) {
	// The proxy answers instead of the (unroutable) target: proof the request
	// really went through the configured proxy URL.
	hit := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- r.Host
		io.WriteString(w, `{"ok":true,"result":[]}`)
	}))
	defer proxy.Close()

	b := newBotClient(testToken, "http://telegram.invalid", proxy.URL)
	if _, err := b.getUpdates(context.Background(), 0, 1); err != nil {
		t.Fatalf("getUpdates through proxy: %v", err)
	}
	select {
	case host := <-hit:
		if host != "telegram.invalid" {
			t.Errorf("proxy saw host %q", host)
		}
	default:
		t.Fatal("the request did not go through the proxy")
	}
}

func TestPollBackoffDoublesAndCaps(t *testing.T) {
	var cur time.Duration
	var seen []int
	for i := 0; i < 9; i++ {
		seen = append(seen, int(pollBackoff(&cur).Seconds()))
	}
	want := []int{1, 2, 4, 8, 16, 32, 60, 60, 60}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("backoff sequence = %v, want %v", seen, want)
		}
	}
}

package telegrambot

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeTG is a stand-in for the Telegram Bot API. It records every call and,
// on every message it is asked to send, enforces the limits the real service
// enforces (4096 characters, 64-byte callback data), so any flow that would be
// rejected in production fails a test here instead.
type fakeTG struct {
	t     *testing.T
	srv   *httptest.Server
	token string

	mu       sync.Mutex
	calls    []fakeCall
	nextMsg  int
	updates  chan tgUpdate
	failNext map[string]int // method -> number of upcoming calls to fail with a 500

	unauthorized atomic.Int32 // requests that carried the wrong bot token
}

type fakeCall struct {
	Method   string
	Body     map[string]any
	File     []byte
	FileName string
}

type fakeButton struct{ Text, Data string }

func newFakeTG(t *testing.T, token string) *fakeTG {
	f := &fakeTG{t: t, token: token, nextMsg: 100, updates: make(chan tgUpdate, 64), failNext: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTG) serve(w http.ResponseWriter, r *http.Request) {
	prefix := "/bot" + f.token + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		f.unauthorized.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		return
	}
	method := strings.TrimPrefix(r.URL.Path, prefix)
	call := fakeCall{Method: method, Body: map[string]any{}}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(64 << 20); err == nil {
			for k, v := range r.MultipartForm.Value {
				call.Body[k] = v[0]
			}
			for _, field := range []string{"document", "photo"} {
				if file, hdr, err := r.FormFile(field); err == nil {
					call.File, _ = io.ReadAll(file)
					call.FileName = hdr.Filename
				}
			}
		}
	} else {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &call.Body)
	}

	if method == "getUpdates" {
		f.serveUpdates(w, r, call)
		return
	}

	f.mu.Lock()
	if n := f.failNext[method]; n > 0 {
		f.failNext[method] = n - 1
		f.mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"ok":false,"error_code":500,"description":"Internal Server Error"}`)
		return
	}
	f.calls = append(f.calls, call)
	f.checkLimits(call)
	result := `true`
	if method == "sendMessage" {
		f.nextMsg++
		result = `{"message_id":` + itoa(f.nextMsg) + `}`
	}
	f.mu.Unlock()
	io.WriteString(w, `{"ok":true,"result":`+result+`}`)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// serveUpdates long-polls for a short while, like the real endpoint does with
// its timeout, so the poll loop under test spins slowly instead of hot.
func (f *fakeTG) serveUpdates(w http.ResponseWriter, r *http.Request, call fakeCall) {
	f.mu.Lock()
	if n := f.failNext["getUpdates"]; n > 0 {
		f.failNext["getUpdates"] = n - 1
		f.calls = append(f.calls, call)
		f.mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"ok":false,"error_code":500,"description":"Internal Server Error"}`)
		return
	}
	f.calls = append(f.calls, call)
	f.mu.Unlock()

	out := []tgUpdate{}
	select {
	case u := <-f.updates:
		out = append(out, u)
	drain:
		for {
			select {
			case u := <-f.updates:
				out = append(out, u)
			default:
				break drain
			}
		}
	case <-time.After(40 * time.Millisecond):
	case <-r.Context().Done():
	}
	raw, _ := json.Marshal(out)
	io.WriteString(w, `{"ok":true,"result":`+string(raw)+`}`)
}

// checkLimits must be called with f.mu held.
func (f *fakeTG) checkLimits(c fakeCall) {
	if c.Method != "sendMessage" && c.Method != "editMessageText" {
		return
	}
	text, _ := c.Body["text"].(string)
	if text == "" {
		f.t.Errorf("%s with empty text", c.Method)
	}
	if n := utf8.RuneCountInString(text); n > 4096 {
		f.t.Errorf("%s text is %d characters, Telegram's limit is 4096", c.Method, n)
	}
	for _, b := range keyboardOf(c) {
		if len(b.Data) > 64 {
			f.t.Errorf("callback data %q is %d bytes, Telegram's limit is 64", b.Data, len(b.Data))
		}
		if b.Data == "" || b.Text == "" {
			f.t.Errorf("button %+v has empty text or data", b)
		}
	}
}

func keyboardOf(c fakeCall) []fakeButton {
	markup, _ := c.Body["reply_markup"].(map[string]any)
	rows, _ := markup["inline_keyboard"].([]any)
	var out []fakeButton
	for _, row := range rows {
		cells, _ := row.([]any)
		for _, cell := range cells {
			m, _ := cell.(map[string]any)
			text, _ := m["text"].(string)
			data, _ := m["callback_data"].(string)
			out = append(out, fakeButton{Text: text, Data: data})
		}
	}
	return out
}

func (f *fakeTG) snapshot() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...)
}

func (f *fakeTG) count(method string) int {
	n := 0
	for _, c := range f.snapshot() {
		if c.Method == method {
			n++
		}
	}
	return n
}

func (f *fakeTG) reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

// lastScreen is the text and buttons of the most recent message shown, whether
// it was sent or edited in place.
func (f *fakeTG) lastScreen() (string, []fakeButton) {
	calls := f.snapshot()
	for i := len(calls) - 1; i >= 0; i-- {
		if c := calls[i]; c.Method == "sendMessage" || c.Method == "editMessageText" {
			text, _ := c.Body["text"].(string)
			return text, keyboardOf(c)
		}
	}
	return "", nil
}

// lastMessageID is the id of the message the console last created.
func (f *fakeTG) lastMessageID() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nextMsg
}

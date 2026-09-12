package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/auth"
)

// newLoggedRouter builds a bare Gin engine carrying only the API-client log
// middleware plus a couple of stand-in routes, so these tests assert what
// the middleware itself emits without dragging a real database in.
func newLoggedRouter(t *testing.T) (*gin.Engine, *bytes.Buffer) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	r := gin.New()
	r.Use(apiClientLogMiddleware(logger))
	r.POST("/api/admin/token", func(c *gin.Context) {
		c.Set(contextLoginUsernameKey, c.PostForm("username"))
		c.Set(contextLoginErrorKey, "wrong password")
		c.JSON(http.StatusUnauthorized, gin.H{"detail": "Incorrect username or password"})
	})
	r.GET("/api/users", func(c *gin.Context) {
		c.Set(auth.ContextAttemptedUsernameKey, "some-reseller")
		c.Set(auth.ContextAuthErrorKey, "token-predates-password-change")
		c.JSON(http.StatusUnauthorized, gin.H{"detail": "Could not validate credentials"})
	})
	r.GET("/api/system", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	return r, buf
}

func TestAPIClientLogNamesTheAccountAndTheExactError(t *testing.T) {
	r, buf := newLoggedRouter(t)

	form := url.Values{"username": {"MehrdadNew"}, "password": {"whatever"}}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Python/3.11 aiohttp/3.13.2")
	req.Header.Set("X-Forwarded-For", "65.109.210.200")
	r.ServeHTTP(httptest.NewRecorder(), req)

	line := buf.String()
	for _, want := range []string{
		apiClientLogTag,
		"MehrdadNew",                     // which account
		"wrong password",                 // why it failed
		"65.109.210.200",                 // which client
		"aiohttp/3.13.2",                 // what software
		"Incorrect username or password", // the exact body it received
	} {
		if !strings.Contains(line, want) {
			t.Errorf("log line is missing %q:\n%s", want, line)
		}
	}
	// A password must never reach the log, however the request failed.
	if strings.Contains(line, "whatever") {
		t.Fatalf("the attempted password leaked into the log:\n%s", line)
	}
}

func TestAPIClientLogRecordsWhyATokenWasRefused(t *testing.T) {
	r, buf := newLoggedRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	req.Header.Set("User-Agent", "VPN-Usage-Tracker/2.0.0")
	r.ServeHTTP(httptest.NewRecorder(), req)

	line := buf.String()
	for _, want := range []string{"some-reseller", "token-predates-password-change", "Could not validate credentials"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line is missing %q:\n%s", want, line)
		}
	}
}

func TestAPIClientLogKeepsBotSuccessesAndSkipsBrowserNoise(t *testing.T) {
	r, buf := newLoggedRouter(t)

	bot := httptest.NewRequest(http.MethodGet, "/api/system", nil)
	bot.Header.Set("User-Agent", "Go-http-client/2.0")
	r.ServeHTTP(httptest.NewRecorder(), bot)
	if !strings.Contains(buf.String(), "Go-http-client") {
		t.Errorf("a successful bot call should be logged:\n%s", buf.String())
	}

	buf.Reset()
	browser := httptest.NewRequest(http.MethodGet, "/api/system", nil)
	browser.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/152.0.0.0")
	r.ServeHTTP(httptest.NewRecorder(), browser)
	if buf.Len() != 0 {
		t.Errorf("a successful dashboard call should stay out of this stream:\n%s", buf.String())
	}

	buf.Reset()
	health := httptest.NewRequest(http.MethodGet, "/health", nil)
	health.Header.Set("User-Agent", "Go-http-client/2.0")
	r.ServeHTTP(httptest.NewRecorder(), health)
	if buf.Len() != 0 {
		t.Errorf("non-/api paths should stay out of this stream:\n%s", buf.String())
	}
}

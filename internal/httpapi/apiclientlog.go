package httpapi

import (
	"bytes"
	"log/slog"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/auth"
)

// apiClientLogTag is the single fixed string every line of this stream
// carries, so the whole bot-facing conversation can be pulled out of a
// busy panel's logs with one grep:
//
//	docker compose -f docker-compose.prod.yml logs -f panel | grep apiclient
const apiClientLogTag = "apiclient"

// maxLoggedBody bounds how much of a failure response is echoed into the
// log. Errors here are short JSON objects; anything longer is a success
// payload we deliberately never log (a user list would dump customer data
// into the log file).
const maxLoggedBody = 512

// captureWriter tees the response body into a buffer so the log can print
// the EXACT error the client received, not a reconstruction of it.
type captureWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *captureWriter) Write(b []byte) (int, error) {
	if w.body.Len() < maxLoggedBody {
		w.body.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *captureWriter) WriteString(s string) (int, error) {
	if w.body.Len() < maxLoggedBody {
		w.body.WriteString(s)
	}
	return w.ResponseWriter.WriteString(s)
}

// apiClientLogMiddleware writes one structured line per API call made by a
// non-browser client, plus one for every API failure whatever the client.
//
// It exists because the ordinary access log (slogMiddleware) records only
// method/path/status, which is exactly not enough when a reseller's bot
// reports "server unavailable": that line cannot say which account the bot
// claimed to be, why its token was refused, or what body it actually got
// back. Every one of those had to be reconstructed by hand from a reverse
// proxy capture. This stream carries all of it at the moment of the
// request.
//
// Deliberately not logged: request bodies (they carry passwords), response
// bodies on success (they carry customer data), and Authorization headers.
func apiClientLogMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Next()
			return
		}

		start := time.Now()
		capture := &captureWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
		c.Writer = capture

		c.Next()

		status := c.Writer.Status()
		ua := c.Request.UserAgent()
		// A browser hitting the dashboard is not what this stream is for -
		// it would bury the handful of bot calls under the dashboard's own
		// polling. Failures are always kept, from any client, since those
		// are the whole point.
		if status < 400 && isBrowserUserAgent(ua) {
			return
		}

		attrs := []any{
			"tag", apiClientLogTag,
			"ip", clientIP(c),
			"ua", ua,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
		}

		// Who the caller is, or - when the call was rejected - who it
		// claimed to be.
		if identity := auth.CurrentIdentity(c); identity != nil {
			attrs = append(attrs, "admin", identity.Username, "is_sudo", identity.IsSudo)
		} else if claimed, ok := c.Get(auth.ContextAttemptedUsernameKey); ok {
			attrs = append(attrs, "admin", claimed)
		} else if username, ok := c.Get(contextLoginUsernameKey); ok {
			attrs = append(attrs, "admin", username)
		}

		if reason, ok := c.Get(auth.ContextAuthErrorKey); ok {
			attrs = append(attrs, "auth_error", reason)
		}
		if reason, ok := c.Get(contextLoginErrorKey); ok {
			attrs = append(attrs, "login_error", reason)
		}

		if status >= 400 {
			attrs = append(attrs, "response", strings.TrimSpace(capture.body.String()))
			logger.Warn(apiClientLogTag, attrs...)
			return
		}
		logger.Info(apiClientLogTag, attrs...)
	}
}

// isBrowserUserAgent is only ever used to decide whether a SUCCESSFUL call
// is worth a line in this stream, so a wrong guess costs at most one noisy
// (or one missing) success entry - never a missing failure.
func isBrowserUserAgent(ua string) bool {
	return strings.Contains(ua, "Mozilla/")
}

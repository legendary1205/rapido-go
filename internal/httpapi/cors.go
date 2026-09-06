package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// CORS mirrors the current app/__init__.py CORSMiddleware setup
// (ALLOWED_ORIGINS env var, credentials allowed, every method/header
// allowed). allowedOrigins of ["*"] reflects whatever Origin the browser
// sent rather than a literal "*", since a literal wildcard is incompatible
// with allow-credentials in every browser.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowAll := len(allowedOrigins) == 1 && allowedOrigins[0] == "*"
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && (allowAll || allowed[origin]) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Vary", "Origin")
		}
		if c.Request.Method == http.MethodOptions {
			c.Header("Access-Control-Allow-Methods", "*")
			c.Header("Access-Control-Allow-Headers", "*")
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

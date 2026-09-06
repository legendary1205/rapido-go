package httpapi

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// parseIDParam reads an int32 path parameter, e.g. ":id".
func parseIDParam(c *gin.Context, name string) (int32, error) {
	n, err := strconv.ParseInt(c.Param(name), 10, 32)
	if err != nil {
		return 0, err
	}
	return int32(n), nil
}

// clientIP mirrors app/routers/admin.py's get_client_ip: the first
// X-Forwarded-For entry if present, else Gin's own client-address
// resolution, else the literal "Unknown".
func clientIP(c *gin.Context) string {
	if fwd := c.GetHeader("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.SplitN(fwd, ",", 2)[0])
	}
	if ip := c.ClientIP(); ip != "" {
		return ip
	}
	return "Unknown"
}

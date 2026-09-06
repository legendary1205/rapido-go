package httpapi

import (
	"strconv"

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

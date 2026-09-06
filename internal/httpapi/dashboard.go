package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// MountDashboardStatic serves the built dashboard (a Vite build's static
// output) at /dashboard/, mirroring the current Python system's own default
// DASHBOARD_PATH. The dashboard uses hash-based client routing (every page
// lives after "#"), so unlike Python's own 404.html-copy SPA-fallback hack,
// a plain static file server is enough - http.FileServer already serves
// index.html for a bare directory request, and no other path under
// /dashboard/ needs special handling since the router never sees anything
// past the "#".
func MountDashboardStatic(r *gin.Engine, dir string) {
	r.StaticFS("/dashboard", http.Dir(dir))
}

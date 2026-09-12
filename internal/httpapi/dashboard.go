package httpapi

import (
	"net/http"
	"path/filepath"

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
//
// Also mounts the same build's statics/ directory (self-hosted fonts, Vite
// copies web/public/ verbatim into the build root) at the site root,
// /statics/ - the customer-facing subscription page's @font-face rules
// reference exactly that path (same origin, no CDN, satisfying its strict
// CSP's font-src 'self'), and that page lives outside /dashboard/'s own
// route tree entirely (it's served from /sub/:token), so it needs this
// second, root-level mount to actually find the font files.
// The site root serves that same build's index.html directly, with a 200 -
// not a redirect to /dashboard/. The real panel answers "/" with the
// dashboard HTML itself, and a client that probes the bare domain to decide
// whether a panel is reachable (several reseller bots do exactly that)
// treats anything other than a 200 as down, redirect or not.
func MountDashboardStatic(r *gin.Engine, dir string) {
	r.StaticFS("/dashboard", http.Dir(dir))
	r.StaticFS("/statics", http.Dir(filepath.Join(dir, "statics")))
	r.GET("/", func(c *gin.Context) {
		c.File(filepath.Join(dir, "index.html"))
	})
}

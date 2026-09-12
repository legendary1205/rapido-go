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
// The dashboard itself is only ever served under /dashboard/ - its asset
// and API base paths are built for that prefix, so serving the same
// index.html at the site root renders a broken page, not a working panel.
// The root therefore answers a deliberately empty 200 document: the same
// thing an admin sees on the real panel when they open the bare domain
// instead of /dashboard (nothing), while still being a 200 for any client
// that probes the root to decide whether the server is reachable.
func MountDashboardStatic(r *gin.Engine, dir string) {
	r.StaticFS("/dashboard", http.Dir(dir))
	r.StaticFS("/statics", http.Dir(filepath.Join(dir, "statics")))
}

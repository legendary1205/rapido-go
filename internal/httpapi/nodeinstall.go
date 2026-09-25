package httpapi

import (
	"bytes"
	_ "embed"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/gin-gonic/gin"
)

// defaultNodeBinDir is where the panel image keeps the node builds.
const defaultNodeBinDir = "/app/nodebin"

// nodeBuildArches are the CPU architectures a node build exists for. The
// requested name only ever selects one of these fixed file names - it is never
// joined into a path as given.
var nodeBuildArches = map[string]bool{"amd64": true, "arm64": true}

//go:embed templates/node-install.sh
var nodeInstallScriptSource string

// The script is served to Linux shells, so a CRLF checkout must not reach them.
var nodeInstallScript = template.Must(template.New("node-install.sh").
	Parse(strings.ReplaceAll(nodeInstallScriptSource, "\r\n", "\n")))

// WithNodeBinDir sets the directory holding the node builds served under
// /install/node; empty keeps the default.
func (h *Handler) WithNodeBinDir(dir string) *Handler {
	h.nodeBinDir = dir
	return h
}

func (h *Handler) binDir() string {
	if h.nodeBinDir == "" {
		return defaultNodeBinDir
	}
	return h.nodeBinDir
}

// registerNodeInstallRoutes wires the one-command node install: the script is
// public (it carries no secret), the builds need a node's own report secret.
func (h *Handler) registerNodeInstallRoutes(r *gin.Engine) {
	r.GET("/install/node.sh", h.handleNodeInstallScript)
	builds := r.Group("/install/node", h.requireNodeSecret)
	builds.GET("/version", h.handleNodeBuildVersion)
	builds.GET("/:arch", h.handleNodeBuild)
}

// A DNS name, IPv4 address or bracketed IPv6 literal, with an optional port.
var originHostPattern = regexp.MustCompile(
	`^(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*|\[[0-9A-Fa-f:.]{2,45}\])(?::([0-9]{1,5}))?$`)

func firstForwardedValue(v string) string {
	first, _, _ := strings.Cut(v, ",")
	return strings.TrimSpace(first)
}

// requestOrigin is the scheme and host the client used to reach the panel,
// taken from the reverse proxy's headers and falling back to the request. The
// result is pasted into a shell script, so anything outside the plain
// characters of a host name is refused rather than escaped.
func requestOrigin(r *http.Request) (string, error) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if v := strings.ToLower(firstForwardedValue(r.Header.Get("X-Forwarded-Proto"))); v != "" {
		if v != "http" && v != "https" {
			return "", errors.New("unsupported X-Forwarded-Proto")
		}
		scheme = v
	}
	host := r.Host
	if v := firstForwardedValue(r.Header.Get("X-Forwarded-Host")); v != "" {
		host = v
	}
	m := originHostPattern.FindStringSubmatch(host)
	if m == nil || len(host) > 255 {
		return "", errors.New("the request's host is not a plain host name")
	}
	if m[1] != "" {
		if port, _ := strconv.Atoi(m[1]); port < 1 || port > 65535 {
			return "", errors.New("the request's host has an invalid port")
		}
	}
	return scheme + "://" + host, nil
}

// handleNodeInstallScript implements GET /install/node.sh (public).
func (h *Handler) handleNodeInstallScript(c *gin.Context) {
	origin, err := requestOrigin(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	var script bytes.Buffer
	if err := nodeInstallScript.Execute(&script, struct{ Origin string }{origin}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not render the install script"})
		return
	}
	// The origin comes from the request, so no cache may reuse this response
	// for anyone else. Plain text, so an admin can read it in a browser first.
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", script.Bytes())
}

// serveNodeFile sends one file of the node build directory. name is always
// built by the caller from fixed parts; a missing file is a 404 that says the
// panel has no build, without revealing where it looks.
func (h *Handler) serveNodeFile(c *gin.Context, name, contentType, what string) {
	f, err := os.Open(filepath.Join(h.binDir(), name))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "This panel has no " + what + " to serve"})
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		c.JSON(http.StatusNotFound, gin.H{"detail": "This panel has no " + what + " to serve"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", contentType)
	// A zero modification time: no Last-Modified, so no client can ever be told
	// "not modified" about a build that has been replaced.
	http.ServeContent(c.Writer, c.Request, name, time.Time{}, f)
}

// handleNodeBuild implements GET /install/node/{amd64|arm64}[.sha256].
func (h *Handler) handleNodeBuild(c *gin.Context) {
	arch, wantChecksum := strings.CutSuffix(c.Param("arch"), ".sha256")
	if !nodeBuildArches[arch] {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Unknown architecture; use amd64 or arm64"})
		return
	}
	name := "rapido-go-node-linux-" + arch
	if wantChecksum {
		h.serveNodeFile(c, name+".sha256", "text/plain; charset=utf-8", "checksum for "+arch)
		return
	}
	h.serveNodeFile(c, name, "application/octet-stream", "node build for "+arch)
}

// handleNodeBuildVersion implements GET /install/node/version.
func (h *Handler) handleNodeBuildVersion(c *gin.Context) {
	f, err := os.Open(filepath.Join(h.binDir(), "VERSION"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "This panel has no node build version to report"})
		return
	}
	defer f.Close()
	raw, _ := io.ReadAll(io.LimitReader(f, 256))
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"version": strings.TrimSpace(string(raw))})
}

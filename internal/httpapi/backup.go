package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
)

// backupFilenamePattern is deliberately an allowlist, not just
// filepath.Base normalization: every backup filename this codebase ever
// produces matches it exactly (see newBackupFilename), so any :filename
// path param that doesn't match is rejected before ever touching the
// filesystem - the strongest available defense against path traversal on
// the download/delete endpoints below. Millisecond precision (not just
// seconds) matters: two backups requested in quick succession must not
// collide on the same filename and silently overwrite one another.
var backupFilenamePattern = regexp.MustCompile(`^rapido_\d{8}T\d{6}\.\d{3}Z\.sql\.gz$`)

const backupTimestampLayout = "20060102T150405.000Z"

func newBackupFilename(t time.Time) string {
	return fmt.Sprintf("rapido_%sZ.sql.gz", t.UTC().Format("20060102T150405.000"))
}

// backupCreatedAt parses the timestamp embedded in a filename this codebase
// generated, rather than trusting filesystem mtime - mtime survives a copy/
// rsync poorly, while the filename's timestamp is authoritative because we
// are the only writer of these files.
func backupCreatedAt(filename string) (time.Time, error) {
	trimmed := filename[len("rapido_") : len(filename)-len(".sql.gz")]
	return time.Parse(backupTimestampLayout, trimmed)
}

type backupInfo struct {
	Filename  string    `json:"filename"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

// listBackupsSorted returns every backup file in dir, newest first. A
// missing directory (nothing has been backed up yet) is not an error - it
// just yields an empty list, matching a fresh install's "no backups yet"
// state rather than requiring the directory to be pre-created out of band.
func listBackupsSorted(dir string) ([]backupInfo, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []backupInfo{}, nil
	}
	if err != nil {
		return nil, err
	}

	backups := make([]backupInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !backupFilenamePattern.MatchString(e.Name()) {
			continue
		}
		createdAt, err := backupCreatedAt(e.Name())
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		backups = append(backups, backupInfo{Filename: e.Name(), SizeBytes: info.Size(), CreatedAt: createdAt})
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].Filename > backups[j].Filename })
	return backups, nil
}

func (h *Handler) handleListBackups(c *gin.Context) {
	backups, err := listBackupsSorted(h.backupDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list backups"})
		return
	}
	c.JSON(http.StatusOK, backups)
}

// dumpDatabaseFn is the shape of h.dumpDatabase - a replaceable field
// (defaulted to execPgDump by NewHandler) rather than a hardcoded method
// call, purely so tests can exercise handleCreateBackup's surrounding
// logic - listing, retention pruning, the HTTP response shape - against a
// trivial fake without needing the real pg_dump binary installed, exactly
// like internal/hostmetrics separates parseX (pure, tested everywhere)
// from readX (the real /proc read, only exercised on Linux). This isolates
// an OS subprocess call; it is not mocking business logic.
type dumpDatabaseFn func(ctx context.Context, w *os.File) error

// execPgDump is the real implementation: runs pg_dump against databaseURL,
// writing a gzip-compressed plain-SQL dump to w.
func execPgDump(ctx context.Context, databaseURL string, w *os.File) error {
	gz := gzip.NewWriter(w)
	cmd := exec.CommandContext(ctx, "pg_dump", databaseURL,
		"--format=plain", "--no-owner", "--no-privileges", "--clean", "--if-exists")
	cmd.Stdout = gz
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		gz.Close()
		return fmt.Errorf("pg_dump: %w: %s", err, stderr.String())
	}
	return gz.Close()
}

func (h *Handler) handleCreateBackup(c *gin.Context) {
	if err := os.MkdirAll(h.backupDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not create the backup directory"})
		return
	}

	filename := newBackupFilename(time.Now())
	path := filepath.Join(h.backupDir, filename)
	f, err := os.Create(path)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not create the backup file"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()
	dumpErr := h.dumpDatabase(ctx, f)
	closeErr := f.Close()
	if dumpErr != nil {
		os.Remove(path)
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Backup failed: " + dumpErr.Error()})
		return
	}
	if closeErr != nil {
		os.Remove(path)
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not finalize the backup file"})
		return
	}

	h.pruneOldBackups()

	info, err := os.Stat(path)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Backup created but could not be read back"})
		return
	}
	createdAt, _ := backupCreatedAt(filename)
	c.JSON(http.StatusOK, backupInfo{Filename: filename, SizeBytes: info.Size(), CreatedAt: createdAt})
}

// pruneOldBackups keeps only the most recent h.backupKeep files, mirroring
// DB_BACKUP_KEEP in the current Python system. Filenames sort
// lexicographically in chronological order (the embedded timestamp is
// fixed-width and UTC), so no separate mtime-based sort is needed.
func (h *Handler) pruneOldBackups() {
	backups, err := listBackupsSorted(h.backupDir)
	if err != nil || h.backupKeep <= 0 || len(backups) <= h.backupKeep {
		return
	}
	for _, b := range backups[h.backupKeep:] {
		os.Remove(filepath.Join(h.backupDir, b.Filename))
	}
}

func (h *Handler) handleDownloadBackup(c *gin.Context) {
	filename := c.Param("filename")
	if !backupFilenamePattern.MatchString(filename) {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "Invalid backup filename"})
		return
	}
	path := filepath.Join(h.backupDir, filename)
	if _, err := os.Stat(path); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Backup not found"})
		return
	}
	c.FileAttachment(path, filename)
}

func (h *Handler) handleDeleteBackup(c *gin.Context) {
	filename := c.Param("filename")
	if !backupFilenamePattern.MatchString(filename) {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "Invalid backup filename"})
		return
	}
	path := filepath.Join(h.backupDir, filename)
	if _, err := os.Stat(path); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Backup not found"})
		return
	}
	if err := os.Remove(path); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not delete backup"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"detail": "Backup removed successfully"})
}

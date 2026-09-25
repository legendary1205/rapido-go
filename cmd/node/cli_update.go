package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// maxNodeBinary bounds a download so a wrong or hostile reply cannot fill the disk.
const maxNodeBinary = 512 << 20

func (c *cli) cmdUpdate(args []string) error {
	if err := c.parse(c.flags("update"), args); err != nil {
		return err
	}
	if err := c.requireHost(); err != nil {
		return err
	}
	return c.update()
}

func (c *cli) update() error {
	panelURL, secret, err := c.installedTarget()
	if err != nil {
		return err
	}
	base := panelURL + "/install/node/" + c.goarch

	c.step("Checking %s for a new build", panelURL)
	sumText, err := c.fetchText(base+".sha256", secret)
	if err != nil {
		return err
	}
	want, err := parseChecksum(sumText)
	if err != nil {
		return err
	}
	if have, err := sha256File(c.paths.Bin); err == nil && have == want {
		c.info("already up to date (sha256 %s...)", want[:12])
		return nil
	}

	c.step("Downloading the new build")
	if err := os.MkdirAll(filepath.Dir(c.paths.Bin), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.paths.Bin), ".rapido-go-node.download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	got, err := c.download(base, secret, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("checksum mismatch: the panel published %s but the download is %s; nothing was changed", want, got)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	// Run the new file before it replaces anything: catches the wrong CPU
	// architecture, a truncated file or a noexec mount while the old build is
	// still in place.
	if out, err := c.shTimeout(20*time.Second, tmp.Name(), "version"); err != nil {
		return fmt.Errorf("the new build failed its self-test and was not installed: %v: %s", err, strings.TrimSpace(out))
	}

	backup := ""
	if old, err := os.ReadFile(c.paths.Bin); err == nil {
		backup = c.paths.Bin + ".bak-" + c.now().UTC().Format("20060102-150405")
		if err := c.writeFile(backup, old, 0o755); err != nil {
			return fmt.Errorf("could not keep a backup of the current build: %w", err)
		}
	}
	if err := os.Rename(tmp.Name(), c.paths.Bin); err != nil {
		return fmt.Errorf("replace %s: %w", c.paths.Bin, err)
	}
	defer c.pruneBackups()

	c.step("Restarting the service")
	restartErr := c.shErr("systemctl", "restart", unitName)
	if restartErr == nil && c.waitServiceActive(20*time.Second, 4) {
		if out, err := c.sh(c.paths.Bin, "version"); err == nil {
			c.info("now running %s", strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
		}
		return nil
	}

	c.warn("the new build did not come up cleanly")
	c.printJournalTail()
	if backup == "" {
		return errors.New("update failed and there was no previous build to roll back to")
	}
	c.step("Rolling back to the previous build")
	old, err := os.ReadFile(backup)
	if err == nil {
		err = c.writeFile(c.paths.Bin, old, 0o755)
	}
	if err != nil {
		return fmt.Errorf("update failed and the rollback failed too (%v); the previous build is %s", err, backup)
	}
	if err := c.shErr("systemctl", "restart", unitName); err != nil || !c.waitServiceActive(20*time.Second, 4) {
		return fmt.Errorf("update failed, and the service is still not running after the rollback; check: journalctl -u %s", unitName)
	}
	return errors.New("update failed; rolled back to the previous build, which is running again")
}

// waitServiceActive is true once the unit has reported active on `stable`
// polls in a row: a build that starts and crashes straight away flips between
// active and activating, and must not count as up.
func (c *cli) waitServiceActive(timeout time.Duration, stable int) bool {
	deadline := c.now().Add(timeout)
	streak := 0
	for {
		if c.serviceState() == "active" {
			if streak++; streak >= stable {
				return true
			}
		} else {
			streak = 0
		}
		if !c.now().Before(deadline) {
			return false
		}
		c.sleep(time.Second)
	}
}

// pruneBackups keeps only the newest keepBackups previous builds. The stamp in
// the name sorts chronologically.
func (c *cli) pruneBackups() {
	matches, _ := filepath.Glob(c.paths.Bin + ".bak-*")
	sort.Strings(matches)
	for len(matches) > keepBackups {
		os.Remove(matches[0])
		matches = matches[1:]
	}
}

// panelError turns a non-200 reply into something an operator can act on.
func panelError(resp *http.Response, what string) error {
	var body struct {
		Detail string `json:"detail"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = json.Unmarshal(raw, &body)
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%s: the panel rejected this node's secret (was the node deleted or re-created? copy the install command from the panel again)", what)
	}
	return fmt.Errorf("%s: HTTP %d %s", what, resp.StatusCode, body.Detail)
}

func (c *cli) fetchText(rawURL, secret string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.panelGet(ctx, rawURL, secret)
	if err != nil {
		return "", fmt.Errorf("cannot reach the panel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", panelError(resp, "checksum request")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return string(raw), err
}

// download streams the body into w and returns its SHA-256.
func (c *cli) download(rawURL, secret string, w io.Writer) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	resp, err := c.panelGet(ctx, rawURL, secret)
	if err != nil {
		return "", fmt.Errorf("cannot reach the panel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", panelError(resp, "download")
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, maxNodeBinary+1))
	if err != nil {
		return "", fmt.Errorf("download interrupted: %w", err)
	}
	if n > maxNodeBinary {
		return "", errors.New("download is larger than any node build should be")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

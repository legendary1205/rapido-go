package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func encodeTestBlob(t *testing.T, b nodeSetupBlob) string {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal test blob: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// TestApplyNodeSetupBlobWritesAllThreeFiles proves the real, load-bearing
// half of the one-paste provisioning flow: a real blob decodes into three
// real files on disk (in a directory that doesn't exist yet, matching a
// genuinely fresh node install) plus the panel URL/report secret fields.
func TestApplyNodeSetupBlobWritesAllThreeFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "rapido-node")
	cfg := config{
		CertFile: filepath.Join(dir, "cert.pem"),
		KeyFile:  filepath.Join(dir, "key.pem"),
		CAFile:   filepath.Join(dir, "ca.pem"),
	}
	blob := encodeTestBlob(t, nodeSetupBlob{
		Cert: "CERT-PEM-DATA", Key: "KEY-PEM-DATA", CA: "CA-PEM-DATA",
		Secret: "the-report-secret", PanelURL: "https://panel.example.test:8001",
	})

	if err := applyNodeSetupBlob(blob, &cfg); err != nil {
		t.Fatalf("applyNodeSetupBlob: %v", err)
	}

	for path, want := range map[string]string{
		cfg.CertFile: "CERT-PEM-DATA",
		cfg.KeyFile:  "KEY-PEM-DATA",
		cfg.CAFile:   "CA-PEM-DATA",
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if cfg.ReportSecret != "the-report-secret" {
		t.Errorf("ReportSecret = %q, want the-report-secret", cfg.ReportSecret)
	}
	if cfg.PanelURL != "https://panel.example.test:8001" {
		t.Errorf("PanelURL = %q, want the blob's panel_url", cfg.PanelURL)
	}
}

// TestApplyNodeSetupBlobOmittedPanelURLKeepsExisting proves panel_url is
// genuinely optional in the blob - an admin who already has PANEL_URL set
// separately (or prefers to keep setting it that way) must not have it
// silently blanked out by a blob that doesn't carry one.
func TestApplyNodeSetupBlobOmittedPanelURLKeepsExisting(t *testing.T) {
	dir := t.TempDir()
	cfg := config{
		CertFile: filepath.Join(dir, "cert.pem"), KeyFile: filepath.Join(dir, "key.pem"), CAFile: filepath.Join(dir, "ca.pem"),
		PanelURL: "https://already-set.example.test",
	}
	blob := encodeTestBlob(t, nodeSetupBlob{Cert: "c", Key: "k", CA: "ca", Secret: "s"})

	if err := applyNodeSetupBlob(blob, &cfg); err != nil {
		t.Fatalf("applyNodeSetupBlob: %v", err)
	}
	if cfg.PanelURL != "https://already-set.example.test" {
		t.Errorf("PanelURL = %q, want the pre-existing value preserved", cfg.PanelURL)
	}
}

func TestApplyNodeSetupBlobRejectsInvalidBase64(t *testing.T) {
	cfg := config{}
	if err := applyNodeSetupBlob("not valid base64!!!", &cfg); err == nil {
		t.Error("expected an error for invalid base64, got nil")
	}
}

func TestApplyNodeSetupBlobRejectsMissingFields(t *testing.T) {
	cfg := config{}
	blob := encodeTestBlob(t, nodeSetupBlob{Cert: "c", Key: "k"}) // missing ca/secret
	if err := applyNodeSetupBlob(blob, &cfg); err == nil {
		t.Error("expected an error for a blob missing ca/secret, got nil")
	}
}

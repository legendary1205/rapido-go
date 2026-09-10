package httpapi

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"testing"
)

func TestCreateNodeIssuesCertSignedByCA(t *testing.T) {
	router, token := newTestRouter(t)

	resp := doRequest(t, router, "POST", "/api/node", token, map[string]interface{}{
		"name": "nod-test", "address": "10.0.0.5", "port": 62050, "api_port": 62051,
	})
	if resp.Code != 200 {
		t.Fatalf("create node: %d %v", resp.Code, resp.Body)
	}

	nodeCertPEM, _ := resp.Body["certificate"].(string)
	caCertPEM, _ := resp.Body["ca_certificate"].(string)
	if nodeCertPEM == "" || caCertPEM == "" {
		t.Fatalf("missing certificate material in response: %v", resp.Body)
	}

	caBlock, _ := pem.Decode([]byte(caCertPEM))
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	nodeBlock, _ := pem.Decode([]byte(nodeCertPEM))
	nodeCert, err := x509.ParseCertificate(nodeBlock.Bytes)
	if err != nil {
		t.Fatalf("parse node cert: %v", err)
	}
	if nodeCert.Subject.CommonName != "nod-test" {
		t.Errorf("node cert CN = %q, want %q", nodeCert.Subject.CommonName, "nod-test")
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := nodeCert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Errorf("issued node certificate does not chain to the returned CA: %v", err)
	}

	node := resp.Body["node"].(map[string]interface{})
	if node["status"] != "connecting" {
		t.Errorf("new node status = %v, want the schema default %q", node["status"], "connecting")
	}
}

// TestCreateNodeSetupBlobMatchesRawFields proves the new one-paste
// provisioning flow: setup_blob decodes to the exact same cert/key/ca/
// secret the response's own separate fields carry (so cmd/node's consumer
// and a manual copy-paste setup can never disagree), and carries the
// optional panel_url through when the admin provided one.
func TestCreateNodeSetupBlobMatchesRawFields(t *testing.T) {
	router, token := newTestRouter(t)

	resp := doRequest(t, router, "POST", "/api/node", token, map[string]interface{}{
		"name": "nod-blob-test", "address": "10.0.0.6", "port": 62050, "api_port": 62051,
		"panel_url": "https://panel.internal.test:8001",
	})
	if resp.Code != 200 {
		t.Fatalf("create node: %d %v", resp.Code, resp.Body)
	}

	blobStr, _ := resp.Body["setup_blob"].(string)
	if blobStr == "" {
		t.Fatalf("setup_blob missing from response: %v", resp.Body)
	}
	raw, err := base64.StdEncoding.DecodeString(blobStr)
	if err != nil {
		t.Fatalf("setup_blob is not valid base64: %v", err)
	}
	var decoded struct {
		Cert     string `json:"cert"`
		Key      string `json:"key"`
		CA       string `json:"ca"`
		Secret   string `json:"secret"`
		PanelURL string `json:"panel_url"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("setup_blob is not valid JSON: %v", err)
	}

	if decoded.Cert != resp.Body["certificate"] {
		t.Errorf("blob cert does not match the response's own certificate field")
	}
	if decoded.Key != resp.Body["key"] {
		t.Errorf("blob key does not match the response's own key field")
	}
	if decoded.CA != resp.Body["ca_certificate"] {
		t.Errorf("blob ca does not match the response's own ca_certificate field")
	}
	if decoded.Secret != resp.Body["report_secret"] {
		t.Errorf("blob secret does not match the response's own report_secret field")
	}
	if decoded.PanelURL != "https://panel.internal.test:8001" {
		t.Errorf("blob panel_url = %q, want the panel_url given at creation", decoded.PanelURL)
	}
}

// TestCreateNodeSetupBlobOmitsPanelURLWhenNotGiven proves panel_url stays
// truly optional - a create request that never mentions it must not embed
// an empty "panel_url":"" the node's own NODE_SETUP_BLOB consumer could
// mistake for "clear the existing PanelURL".
func TestCreateNodeSetupBlobOmitsPanelURLWhenNotGiven(t *testing.T) {
	router, token := newTestRouter(t)

	resp := doRequest(t, router, "POST", "/api/node", token, map[string]interface{}{
		"name": "nod-blob-no-url", "address": "10.0.0.7", "port": 62050, "api_port": 62051,
	})
	if resp.Code != 200 {
		t.Fatalf("create node: %d %v", resp.Code, resp.Body)
	}
	blobStr, _ := resp.Body["setup_blob"].(string)
	raw, _ := base64.StdEncoding.DecodeString(blobStr)
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("setup_blob is not valid JSON: %v", err)
	}
	if _, present := decoded["panel_url"]; present {
		t.Errorf("panel_url key present in the blob despite never being given: %v", decoded)
	}
}

func TestCreateNodeDuplicateNameRejected(t *testing.T) {
	router, token := newTestRouter(t)
	body := map[string]interface{}{"name": "dup-node", "address": "10.0.0.6", "port": 1, "api_port": 2}
	doRequest(t, router, "POST", "/api/node", token, body)
	resp := doRequest(t, router, "POST", "/api/node", token, body)
	if resp.Code != 409 {
		t.Fatalf("expected 409 for duplicate node name, got %d %v", resp.Code, resp.Body)
	}
}

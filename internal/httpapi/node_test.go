package httpapi

import (
	"crypto/x509"
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

func TestCreateNodeDuplicateNameRejected(t *testing.T) {
	router, token := newTestRouter(t)
	body := map[string]interface{}{"name": "dup-node", "address": "10.0.0.6", "port": 1, "api_port": 2}
	doRequest(t, router, "POST", "/api/node", token, body)
	resp := doRequest(t, router, "POST", "/api/node", token, body)
	if resp.Code != 409 {
		t.Fatalf("expected 409 for duplicate node name, got %d %v", resp.Code, resp.Body)
	}
}

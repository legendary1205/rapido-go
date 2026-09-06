package certs

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

func TestGenerateCA(t *testing.T) {
	pair, key, err := GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	block, _ := pem.Decode([]byte(pair.CertPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("CertPEM did not decode to a CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	if cert.Subject.CommonName != CommonName {
		t.Errorf("CommonName = %q, want %q", cert.Subject.CommonName, CommonName)
	}
	if !cert.IsCA {
		t.Error("IsCA = false, want true")
	}
	if key.PublicKey.Size()*8 < keyBits {
		t.Errorf("key size = %d bits, want at least %d", key.PublicKey.Size()*8, keyBits)
	}

	// Self-signed: verifying the certificate's signature against its own
	// public key must succeed.
	if err := cert.CheckSignatureFrom(cert); err != nil {
		t.Errorf("certificate is not validly self-signed: %v", err)
	}

	wantExpiry := time.Now().UTC().Add(validFor)
	if diff := cert.NotAfter.Sub(wantExpiry); diff > time.Hour || diff < -time.Hour {
		t.Errorf("NotAfter = %v, want ~%v", cert.NotAfter, wantExpiry)
	}
}

func TestSignNodeCert(t *testing.T) {
	ca, caKey, err := GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	node, err := SignNodeCert("nod1", ca.CertPEM, caKey)
	if err != nil {
		t.Fatalf("SignNodeCert: %v", err)
	}

	caBlock, _ := pem.Decode([]byte(ca.CertPEM))
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate(CA): %v", err)
	}

	nodeBlock, _ := pem.Decode([]byte(node.CertPEM))
	if nodeBlock == nil {
		t.Fatal("node CertPEM did not decode")
	}
	nodeCert, err := x509.ParseCertificate(nodeBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate(node): %v", err)
	}

	if nodeCert.Subject.CommonName != "nod1" {
		t.Errorf("node CommonName = %q, want %q", nodeCert.Subject.CommonName, "nod1")
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := nodeCert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Errorf("node certificate does not chain to the Rapido CA: %v", err)
	}
}

func TestSignNodeCertRejectsInvalidCAPEM(t *testing.T) {
	_, caKey, err := GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	if _, err := SignNodeCert("nod1", "not a pem", caKey); err == nil {
		t.Error("SignNodeCert with invalid CA PEM succeeded, want error")
	}
}

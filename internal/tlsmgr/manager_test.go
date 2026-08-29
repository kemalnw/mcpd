package tlsmgr

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileModeLoadsCertificateIntoTLSConfig(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")
	writeSelfSignedPair(t, certPath, keyPath, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	m, err := New(Options{Mode: "files", CertFile: certPath, KeyFile: keyPath, RenewCheck: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg := m.TLSConfig()
	if cfg.MinVersion == 0 {
		t.Fatal("TLS minimum version is unset")
	}
	cert, err := cfg.GetCertificate(nil)
	if err != nil || cert == nil || cert.Leaf == nil {
		t.Fatalf("GetCertificate: cert=%#v err=%v", cert, err)
	}
}

func TestRenewalWindowBeginsAtHalfLife(t *testing.T) {
	m := &Manager{}
	now := time.Now().UTC()
	m.current.Store(tlsCertificateWithLeaf(now.Add(-24*time.Hour), now.Add(72*time.Hour)))
	if m.needsRenewal(now) {
		t.Fatal("certificate renewed before half-life")
	}
	m.current.Store(tlsCertificateWithLeaf(now.Add(-72*time.Hour), now.Add(24*time.Hour)))
	if !m.needsRenewal(now) {
		t.Fatal("certificate did not renew after half-life")
	}
}

func TestCertificateHostSupportsRawPublicIP(t *testing.T) {
	host, err := certificateHost("https://203.0.113.10")
	if err != nil || host != "203.0.113.10" {
		t.Fatalf("host=%q err=%v", host, err)
	}
	host, err = certificateHost("https://mcp.example:8443")
	if err != nil || host != "mcp.example" {
		t.Fatalf("host=%q err=%v", host, err)
	}
}

func TestACMEAccountKeyPersistsWithPrivatePermissions(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{opts: Options{CertDir: dir}}
	first, err := m.loadOrCreateAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.loadOrCreateAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	if first.D.Cmp(second.D) != 0 {
		t.Fatal("ACME account key was not reused")
	}
	info, err := os.Stat(filepath.Join(dir, "acme-account.key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("account key mode = %o", info.Mode().Perm())
	}
}

func tlsCertificateWithLeaf(notBefore, notAfter time.Time) *tls.Certificate {
	return &tls.Certificate{Leaf: &x509.Certificate{NotBefore: notBefore, NotAfter: notAfter}}
}

func writeSelfSignedPair(t *testing.T, certPath, keyPath string, notBefore, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"}, NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
}

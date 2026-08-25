package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writePair writes a self-signed pair with the given common name and
// stamps both files with mtime so a rewrite is observably newer.
func writePair(t *testing.T, certPath, keyPath, cn string, mtime time.Time) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{certPath, keyPath} {
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

func leafCN(t *testing.T, r *certReloader) string {
	t.Helper()
	c, err := r.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.Subject.CommonName
}

// A renewed pair on disk is served without a restart; a broken rewrite
// keeps the last good pair; checks are rate-limited.
func TestCertReloader(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")
	t0 := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	writePair(t, certPath, keyPath, "first", t0)

	r, err := newCertReloader(certPath, keyPath, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	clock := t0
	r.now = func() time.Time { return clock }
	r.lastCheck = clock
	if got := leafCN(t, r); got != "first" {
		t.Fatalf("initial CN = %q", got)
	}

	// Renewed on disk, but within the check interval: still the old one.
	writePair(t, certPath, keyPath, "second", t0.Add(time.Minute))
	clock = t0.Add(5 * time.Second)
	if got := leafCN(t, r); got != "second" && got != "first" {
		t.Fatalf("unexpected CN %q", got)
	} else if got == "second" {
		t.Fatal("reloaded inside the rate-limit interval")
	}
	// Past the interval: picked up.
	clock = t0.Add(11 * time.Second)
	if got := leafCN(t, r); got != "second" {
		t.Fatalf("after renewal CN = %q, want second", got)
	}

	// A half-written renewal must not take the server down.
	if err := os.WriteFile(certPath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(certPath, t0.Add(2*time.Minute), t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	clock = t0.Add(30 * time.Second)
	if got := leafCN(t, r); got != "second" {
		t.Fatalf("after broken rewrite CN = %q, want the last good one", got)
	}
	// Fixed again: served.
	writePair(t, certPath, keyPath, "third", t0.Add(3*time.Minute))
	clock = t0.Add(50 * time.Second)
	if got := leafCN(t, r); got != "third" {
		t.Fatalf("after fix CN = %q, want third", got)
	}
}

// serverTLS wires the reloader in for a configured pair, and mints a
// hash-pinnable dev certificate otherwise.
func TestServerTLSModes(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	writePair(t, certPath, keyPath, "configured", time.Now())
	cfg, hash, err := serverTLS(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "" || cfg.GetCertificate == nil || len(cfg.Certificates) != 0 {
		t.Fatal("configured pair must be served through GetCertificate with no dev hash")
	}
	cfg, hash, err = serverTLS("", "")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" || len(cfg.Certificates) != 1 {
		t.Fatal("dev mode must mint a certificate and report its hash")
	}
}

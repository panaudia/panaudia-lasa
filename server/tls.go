package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"os"
	"sync"
	"time"

	"github.com/quic-go/quic-go/http3"

	"github.com/panaudia/lasa/server"
)

// certReloadInterval bounds how often a handshake re-checks the
// certificate files' modification times — a stat per handshake would be
// fine, a stat per ten seconds is invisible.
const certReloadInterval = 10 * time.Second

// serverTLS serves the configured certificate, re-reading the files
// when they change so a renewal (Let's Encrypt on the host, a mounted
// secret in an orchestrator) takes effect with no restart and no
// dropped sessions; or it mints an ephemeral self-signed one when none
// is configured (dev mode). The dev cert is ECDSA P-256 with a ≤14-day
// validity — the exact shape browsers accept for WebTransport's
// serverCertificateHashes, so a browser client can pin the returned
// hash instead of needing a CA (Go test clients just skip
// verification). certHash is the base64 SHA-256 of the cert DER, empty
// when a real certificate is configured.
func serverTLS(certFile, keyFile string) (cfg *tls.Config, certHash string, err error) {
	// Raw-QUIC MoQ and HTTP/3 (WebTransport) share the listener; the
	// serve loop routes by the negotiated protocol.
	cfg = &tls.Config{NextProtos: []string{server.ALPN, http3.NextProtoH3}}
	if certFile != "" {
		r, err := newCertReloader(certFile, keyFile, certReloadInterval)
		if err != nil {
			return nil, "", err
		}
		cfg.GetCertificate = r.GetCertificate
		return cfg, "", nil
	}
	cert, err := selfSigned()
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(cert.Certificate[0])
	cfg.Certificates = []tls.Certificate{cert}
	return cfg, base64.StdEncoding.EncodeToString(sum[:]), nil
}

// certReloader hands out the certificate pair from disk, re-reading it
// when either file's modification time changes. A pair that fails to
// load is logged and the last good one stays in service — a half-written
// renewal never takes the server down.
type certReloader struct {
	certFile, keyFile string
	minInterval       time.Duration
	now               func() time.Time

	mu        sync.Mutex
	cert      *tls.Certificate
	certMod   time.Time
	keyMod    time.Time
	lastCheck time.Time
}

func newCertReloader(certFile, keyFile string, minInterval time.Duration) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile, minInterval: minInterval, now: time.Now}
	cert, cm, km, err := r.load()
	if err != nil {
		return nil, err
	}
	r.cert, r.certMod, r.keyMod, r.lastCheck = cert, cm, km, r.now()
	return r, nil
}

func (r *certReloader) load() (*tls.Certificate, time.Time, time.Time, error) {
	ci, err := os.Stat(r.certFile)
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	ki, err := os.Stat(r.keyFile)
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	if cert.Leaf == nil {
		if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil {
			cert.Leaf = leaf
		}
	}
	return &cert, ci.ModTime(), ki.ModTime(), nil
}

// GetCertificate is the tls.Config hook: called per handshake, on the
// handshake's goroutine.
func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if now.Sub(r.lastCheck) >= r.minInterval {
		r.lastCheck = now
		r.maybeReload()
	}
	return r.cert, nil
}

// maybeReload runs under r.mu.
func (r *certReloader) maybeReload() {
	ci, err1 := os.Stat(r.certFile)
	ki, err2 := os.Stat(r.keyFile)
	if err1 != nil || err2 != nil {
		slog.Warn("tls: certificate files unreadable; keeping the loaded certificate", "cert", r.certFile, "key", r.keyFile)
		return
	}
	if ci.ModTime().Equal(r.certMod) && ki.ModTime().Equal(r.keyMod) {
		return
	}
	cert, cm, km, err := r.load()
	if err != nil {
		slog.Warn("tls: certificate changed on disk but failed to load; keeping the loaded certificate", "err", err)
		return
	}
	r.cert, r.certMod, r.keyMod = cert, cm, km
	attrs := []any{"cert", r.certFile}
	if cert.Leaf != nil {
		attrs = append(attrs, "notAfter", cert.Leaf.NotAfter, "subject", cert.Leaf.Subject.String())
	}
	slog.Info("tls: certificate reloaded", attrs...)
}

func selfSigned() (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "panaudia-server dev"},
		NotBefore:    now.Add(-time.Hour),
		// WebTransport's serverCertificateHashes requires ≤ 14 days.
		NotAfter:    now.Add(13 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	key, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}),
	)
}

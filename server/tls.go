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
	"math/big"
	"net"
	"time"

	"github.com/quic-go/quic-go/http3"

	"github.com/panaudia/lasa/server"
)

// serverTLS loads the configured certificate, or mints an ephemeral
// self-signed one when none is configured (dev mode). The dev cert is
// ECDSA P-256 with a ≤14-day validity — the exact shape browsers accept
// for WebTransport's serverCertificateHashes, so a browser client can
// pin the returned hash instead of needing a CA (Go test clients just
// skip verification). certHash is the base64 SHA-256 of the cert DER,
// empty when a real certificate is configured.
func serverTLS(certFile, keyFile string) (cfg *tls.Config, certHash string, err error) {
	var cert tls.Certificate
	if certFile != "" {
		cert, err = tls.LoadX509KeyPair(certFile, keyFile)
	} else {
		cert, err = selfSigned()
		if err == nil {
			sum := sha256.Sum256(cert.Certificate[0])
			certHash = base64.StdEncoding.EncodeToString(sum[:])
		}
	}
	if err != nil {
		return nil, "", err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		// Raw-QUIC MoQ and HTTP/3 (WebTransport) share the listener;
		// the serve loop routes by the negotiated protocol.
		NextProtos: []string{server.ALPN, http3.NextProtoH3},
	}, certHash, nil
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

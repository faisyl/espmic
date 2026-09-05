package server

import (
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
	"testing"
	"time"

	"espmic/server/internal/config"
)

func generateSelfSignedCert(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	certOut, err := os.CreateTemp(dir, "cert*.pem")
	if err != nil {
		t.Fatal(err)
	}
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	certOut.Close()
	keyOut, err := os.CreateTemp(dir, "key*.pem")
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	keyOut.Close()
	return certOut.Name(), keyOut.Name()
}

func TestStartControlListenerTLS(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := generateSelfSignedCert(t, dir)
	cfg := config.Load()
	cfg.ControlAddr = "localhost:0"
	cfg.TLSCertFile = certFile
	cfg.TLSKeyFile = keyFile
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Start() }()
	time.Sleep(100 * time.Millisecond)
	addr := srv.controlLn.Addr().String()
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}
	_ = conn.Close()
	srv.cancel()
}

func TestStartControlListenerPlainTCP(t *testing.T) {
	cfg := config.Load()
	cfg.ControlAddr = "localhost:0"
	cfg.TLSCertFile = ""
	cfg.TLSKeyFile = ""
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Start() }()
	time.Sleep(100 * time.Millisecond)
	addr := srv.controlLn.Addr().String()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("net.Dial: %v", err)
	}
	_ = conn.Close()
	srv.cancel()
}

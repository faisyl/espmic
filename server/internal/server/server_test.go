package server

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
	"testing"
	"time"

	"espmic/server/internal/config"
	"espmic/server/internal/device"
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

func TestAuthenticateOpenEnrollment(t *testing.T) {
	cfg := config.Load()
	cfg.DeviceCredential = "" // open enrollment
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Unknown device with no credential configured -> accepted (TOFU)
	if err := srv.Authenticate(ctx, "esp32-new", ""); err != nil {
		t.Fatalf("Authenticate open TOFU: %v", err)
	}
	// Device should now be registered
	devs := srv.DeviceList().([]device.Device)
	if len(devs) != 1 || devs[0].DeviceID != "esp32-new" {
		t.Fatalf("expected device enrolled, got %v", devs)
	}

	// Same device again -> accepted
	if err := srv.Authenticate(ctx, "esp32-new", ""); err != nil {
		t.Fatalf("Authenticate existing: %v", err)
	}

	// Another new device -> accepted
	if err := srv.Authenticate(ctx, "esp32-other", "anything"); err != nil {
		t.Fatalf("Authenticate second TOFU: %v", err)
	}
	devs = srv.DeviceList().([]device.Device)
	if len(devs) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(devs))
	}
}

func TestAuthenticateWithCredential(t *testing.T) {
	cfg := config.Load()
	cfg.DeviceCredential = "shared-secret-123"
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Correct credential -> accepted + enrolled
	if err := srv.Authenticate(ctx, "esp32-cred", "shared-secret-123"); err != nil {
		t.Fatalf("Authenticate correct cred: %v", err)
	}
	devs := srv.DeviceList().([]device.Device)
	if len(devs) != 1 || devs[0].DeviceID != "esp32-cred" {
		t.Fatalf("expected device enrolled, got %v", devs)
	}

	// Wrong credential -> rejected
	if err := srv.Authenticate(ctx, "esp32-wrong", "wrong-secret"); err != device.ErrAuthFailed {
		t.Fatalf("expected ErrAuthFailed, got %v", err)
	}
	// Device not enrolled
	devs = srv.DeviceList().([]device.Device)
	if len(devs) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devs))
	}

	// Empty credential -> rejected (constant-time compare with empty string)
	if err := srv.Authenticate(ctx, "esp32-empty", ""); err != device.ErrAuthFailed {
		t.Fatalf("expected ErrAuthFailed for empty cred, got %v", err)
	}

	// Existing device with correct credential -> accepted
	if err := srv.Authenticate(ctx, "esp32-cred", "shared-secret-123"); err != nil {
		t.Fatalf("Authenticate existing with cred: %v", err)
	}
}

func TestAuthenticateConstantTimeCompare(t *testing.T) {
	// Verify constant-time compare behavior (no timing leak in test, just correctness)
	cfg := config.Load()
	cfg.DeviceCredential = "secret"
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Different length should fail (constant-time compare handles this)
	if err := srv.Authenticate(ctx, "dev1", "secret-extra"); err != device.ErrAuthFailed {
		t.Fatalf("expected ErrAuthFailed for longer string")
	}
	if err := srv.Authenticate(ctx, "dev2", "secr"); err != device.ErrAuthFailed {
		t.Fatalf("expected ErrAuthFailed for shorter string")
	}
}

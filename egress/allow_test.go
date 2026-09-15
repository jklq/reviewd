package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testCA(t *testing.T) (string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "reviewd egress test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_ = pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	_ = pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	_ = f.Close()
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return path, pool
}

func TestAllowlistMatch(t *testing.T) {
	a := parseAllow([]string{"api.example.com", "*.wildcard.test"}, "extra.example.com, *.env.test")
	for _, host := range []string{"api.example.com", "deep.sub.wildcard.test", "wildcard.test", "extra.example.com", "a.env.test", "API.EXAMPLE.COM.", " api.example.com "} {
		if !a.Match(host) {
			t.Fatalf("rejected %q", host)
		}
	}
	for _, host := range []string{"", "evil.com", "example.com", "api.example.com.evil.com", "notwildcard.test"} {
		if a.Match(host) {
			t.Fatalf("allowed %q", host)
		}
	}
	if parseAllow(nil, "").Match("anything.test") {
		t.Fatal("empty allowlist matches")
	}
}

func TestLeafVerifiesAgainstCA(t *testing.T) {
	path, pool := testCA(t)
	authority, err := loadCA(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"api.example.com", "127.0.0.1"} {
		leaf, err := authority.leaf(host)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := leaf.Leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: pool, CurrentTime: time.Now()}); err != nil {
			t.Fatalf("leaf for %s does not verify: %v", host, err)
		}
	}
	again, err := authority.leaf("api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	first, err := authority.leaf("api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatal("leaf not cached")
	}
}

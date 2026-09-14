package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testCABundle = `-----BEGIN CERTIFICATE-----
QUJD
-----END CERTIFICATE-----
-----BEGIN PRIVATE KEY-----
REVG
-----END PRIVATE KEY-----
`

func TestSplitCABundle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, []byte(testCABundle), 0600); err != nil {
		t.Fatal(err)
	}
	certs, hasKey, err := SplitCABundle(path)
	if err != nil {
		t.Fatal(err)
	}
	if !hasKey {
		t.Fatal("key not detected")
	}
	if !strings.Contains(string(certs), "BEGIN CERTIFICATE") {
		t.Fatalf("cert missing: %q", certs)
	}
	if strings.Contains(string(certs), "PRIVATE KEY") {
		t.Fatalf("key leaked into cert output: %q", certs)
	}
}

func TestSplitCABundleErrors(t *testing.T) {
	dir := t.TempDir()
	certOnly := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(certOnly, []byte("-----BEGIN CERTIFICATE-----\nQUJD\n-----END CERTIFICATE-----\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, hasKey, err := SplitCABundle(certOnly); err != nil || hasKey {
		t.Fatalf("cert-only: hasKey=%v err=%v", hasKey, err)
	}
	keyOnly := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(keyOnly, []byte("-----BEGIN PRIVATE KEY-----\nREVG\n-----END PRIVATE KEY-----\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SplitCABundle(keyOnly); err == nil {
		t.Fatal("key-only bundle accepted")
	}
	if _, _, err := SplitCABundle(filepath.Join(dir, "missing.pem")); err == nil {
		t.Fatal("missing file accepted")
	}
}

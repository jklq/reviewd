package github

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJWTAndInstallationToken(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tokens := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/7/access_tokens" {
			tokens++
			parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
			if len(parts) != 3 {
				t.Error("invalid jwt")
				w.WriteHeader(401)
				return
			}
			sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
			sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "installation-token", "expires_at": time.Now().Add(time.Hour)})
		} else {
			if r.Header.Get("Authorization") != "Bearer installation-token" {
				t.Error("wrong token")
			}
			w.WriteHeader(204)
		}
	}))
	defer api.Close()
	app := &App{ID: 1, Key: key, BaseURL: api.URL, HTTP: api.Client()}
	c := app.Installation(7)
	for i := 0; i < 2; i++ {
		if err := c.Do(context.Background(), "GET", "/resource", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if tokens != 1 {
		t.Fatal(tokens)
	}
}
func archive(t *testing.T, headers []*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write(bytes.Repeat([]byte("x"), int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestArchiveExtraction(t *testing.T) {
	d := t.TempDir()
	b := archive(t, []*tar.Header{{Name: "repo/file", Typeflag: tar.TypeReg, Size: 1, Mode: 04777}, {Name: "repo/link", Typeflag: tar.TypeSymlink, Linkname: "file"}})
	if err := Extract(bytes.NewReader(b), d); err != nil {
		t.Fatal(err)
	}
	s, err := os.Stat(filepath.Join(d, "file"))
	if err != nil || s.Mode()&os.ModeSetuid != 0 {
		t.Fatal("unsafe mode", err)
	}
	for _, h := range []*tar.Header{{Name: "repo/../../escape", Typeflag: tar.TypeReg}, {Name: "repo/link", Typeflag: tar.TypeSymlink, Linkname: "../../escape"}, {Name: "repo/fifo", Typeflag: tar.TypeFifo}} {
		if err := Extract(bytes.NewReader(archive(t, []*tar.Header{h})), t.TempDir()); err == nil {
			t.Fatal("unsafe archive accepted", h)
		}
	}
}

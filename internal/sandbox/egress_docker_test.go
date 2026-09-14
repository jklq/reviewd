package sandbox_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reviewd/internal/config"
	"reviewd/internal/credential"
	"reviewd/internal/sandbox"
)

const egressRealCredential = "test-access-egress"

func egressTestCA(t *testing.T) (*x509.Certificate, crypto.Signer, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "reviewd egress integration CA"},
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
	bundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	bundle = append(bundle, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
	return cert, key, bundle
}

func egressLeaf(t *testing.T, ca *x509.Certificate, key crypto.Signer, dns string) []byte {
	t.Helper()
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: dns},
		DNSNames:     []string{dns},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	bundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return append(bundle, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
}

func dockerOutput(ctx context.Context, args ...string) (string, error) {
	b, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return strings.TrimSpace(string(b)), err
}

func TestDockerEgressPipeline(t *testing.T) {
	if os.Getenv("REVIEWD_DOCKER_TEST") != "1" {
		t.Skip("set REVIEWD_DOCKER_TEST=1 to run Docker integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if _, err := dockerOutput(ctx, "image", "inspect", "reviewd-egress:local"); err != nil {
		t.Fatal("reviewd-egress:local missing; run make egress-image")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "reviewd")
	build := exec.Command("go", "build", "-o", binary, "./cmd/reviewd")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", b, err)
	}
	stub := filepath.Join(dir, "stub")
	buildStub := exec.Command("go", "build", "-o", stub, "./internal/sandbox/echostub")
	buildStub.Dir = root
	buildStub.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := buildStub.CombinedOutput(); err != nil {
		t.Fatalf("build stub: %s %v", b, err)
	}
	if _, err := dockerOutput(ctx, "pull", "alpine:3.21"); err != nil {
		t.Fatalf("pull: %v", err)
	}
	harnessImage := "reviewd-egress-test-harness:local"
	dockerfile := "FROM alpine:3.21\nRUN apk add --no-cache curl ca-certificates\n"
	buildImage := exec.CommandContext(ctx, "docker", "build", "-t", harnessImage, "-f", "-", dir)
	buildImage.Dir = root
	buildImage.Stdin = strings.NewReader(dockerfile)
	if b, err := buildImage.CombinedOutput(); err != nil {
		t.Fatalf("build harness image: %s %v", b, err)
	}
	ca, caKey, caBundle := egressTestCA(t)
	caFile := filepath.Join(dir, "egress-ca.pem")
	if err := os.WriteFile(caFile, caBundle, 0600); err != nil {
		t.Fatal(err)
	}
	stubCert := filepath.Join(dir, "stub.pem")
	if err := os.WriteFile(stubCert, egressLeaf(t, ca, caKey, "upstream"), 0600); err != nil {
		t.Fatal(err)
	}
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(nonce[:])
	upstreamNet := "reviewd-test-upstream-" + suffix
	stubName := "reviewd-test-stub-" + suffix
	if out, err := dockerOutput(ctx, "network", "create", upstreamNet); err != nil {
		t.Fatalf("create upstream net: %s %v", out, err)
	}
	t.Cleanup(func() { _, _ = dockerOutput(context.Background(), "network", "rm", upstreamNet) })
	stubArgs := []string{"run", "-d", "--rm", "--name", stubName, "--network", upstreamNet, "--network-alias", "upstream",
		"--mount", "type=bind,src=" + stub + ",dst=/stub,readonly", "--mount", "type=bind,src=" + stubCert + ",dst=/certs/stub.pem,readonly",
		"alpine:3.21", "/stub", "--http", ":8080", "--https", ":8443", "--cert", "/certs/stub.pem"}
	if out, err := dockerOutput(ctx, stubArgs...); err != nil {
		t.Fatalf("start stub: %s %v", out, err)
	}
	t.Cleanup(func() { _, _ = dockerOutput(context.Background(), "rm", "-f", stubName) })
	deadline := time.Now().Add(30 * time.Second)
	for {
		logs, _ := dockerOutput(ctx, "logs", stubName)
		if strings.Contains(logs, "stub listening") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stub never started: %q", logs)
		}
		time.Sleep(250 * time.Millisecond)
	}
	head := filepath.Join(dir, "head")
	base := filepath.Join(dir, "base")
	for _, p := range []string{head, base} {
		if err = os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(head, "store.go"), []byte("package store\nfunc save() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "credentials", "login.json")
	if err := os.MkdirAll(filepath.Dir(state), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("login-state"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "reviewd.json")
	script := `set -eu
export https_proxy=$HTTPS_PROXY http_proxy=$HTTP_PROXY no_proxy=$NO_PROXY
test -n "$HARNESS_AUTH"
case "$HARNESS_AUTH" in reviewd-sentinel-*) ;; *) echo "harness auth is not a sentinel"; exit 10;; esac
if [ "$HARNESS_AUTH" = 'test-access-egress' ]; then echo "harness holds real credential"; exit 11; fi
test -s /review/egress-ca.pem
test "$REVIEWD_EGRESS_CA" = /review/egress-ca.pem
if env -u HTTPS_PROXY -u HTTP_PROXY -u NO_PROXY -u https_proxy -u http_proxy -u no_proxy curl -sS --max-time 5 http://upstream:8080/echo 2>/dev/null; then echo "upstream directly reachable"; exit 12; fi
AUTH=$(curl -sS --max-time 15 --cacert "$REVIEWD_EGRESS_CA" -H "Authorization: Bearer $HARNESS_AUTH" https://upstream:8443/echo)
case "$AUTH" in *"$HARNESS_AUTH"*) ;; *) echo "response not scrubbed"; exit 13;; esac
case "$AUTH" in *test-access-egress*) echo "real credential in response"; exit 14;; esac
PLAIN=$(curl -sS --max-time 15 -H "Authorization: Bearer $HARNESS_AUTH" http://upstream:8080/echo)
case "$PLAIN" in *test-access-egress*) echo "real credential in plain response"; exit 15;; esac
case "$PLAIN" in *"$HARNESS_AUTH"*) ;; *) echo "plain response not scrubbed"; exit 16;; esac
POSTED=$(curl -sS --max-time 15 --cacert "$REVIEWD_EGRESS_CA" -H "X-Token: $HARNESS_AUTH" --data "login $HARNESS_AUTH" https://upstream:8443/echo)
case "$POSTED" in *test-access-egress*) echo "real credential in post response"; exit 17;; esac
if curl -sS --max-time 15 --fail https://blocked.test/ >/dev/null 2>&1; then echo "allowlist bypassed"; exit 18; fi
reviewd agent init
reviewd agent finding --path store.go --line 2 --side RIGHT --priority 2 --confidence 0.98 --title 'Handle persistence failures' --body 'Disk failures lose acknowledged updates. Propagate the write error.' --evidence 'The save return value is ignored.'
cat >/tmp/overview.json <<'JSON'
{"summary":"Persist updates","confidence":2,"reasons":["Write failures lose updates."],"important_files":[{"path":"store.go","description":"Adds persistence."}],"sequence_diagram":"sequenceDiagram\n Caller->>Store: Save"}
JSON
reviewd agent overview --file /tmp/overview.json
reviewd agent submit
`
	doc := map[string]any{
		"data_dir":               dir,
		"timeout":                "2m",
		"memory":                 "512m",
		"cpus":                   "1",
		"reviewers":              []string{"mock"},
		"validator":              "mock",
		"parallelism":            1,
		"workers":                1,
		"max_findings":           20,
		"min_finding_confidence": 0.85,
		"harnesses": map[string]any{
			"mock": map[string]any{
				"image":       harnessImage,
				"command":     []string{"sh", "-c", script, "mock", "{{.PromptFile}}"},
				"credentials": []string{"shared"},
				"egress":      true,
			},
		},
		"credentials": map[string]any{
			"shared": map[string]any{
				"state_file": state,
				"exports":    []string{"HARNESS_AUTH"},
				"command":    []string{"sh", "-c", `printf '%s' '{"expires_at":"2035-01-01T00:00:00Z","env":{"HARNESS_AUTH":"test-access-egress"}}'`},
			},
		},
		"egress": map[string]any{
			"image":   "reviewd-egress:local",
			"command": []string{"/usr/local/bin/egress", "serve", "--listen", ":8080", "--allow", "upstream", "--config", configPath},
			"network": upstreamNet,
			"ca_file": caFile,
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	owner := sha256.Sum256([]byte(dir))
	runner := sandbox.Docker{Binary: binary, Memory: "512m", CPUs: "1", Owner: hex.EncodeToString(owner[:16]), Credentials: credential.New(c), Egress: c.Egress, ConfigFile: c.ConfigFile}
	assertClean := func() {
		t.Helper()
		left, err := dockerOutput(ctx, "ps", "-aq", "--filter", "label=reviewd.owner="+runner.Owner)
		if err != nil || left != "" {
			t.Fatalf("containers remained: %q %v", left, err)
		}
		nets, err := dockerOutput(ctx, "network", "ls", "-q", "--filter", "label=reviewd.owner="+runner.Owner)
		if err != nil || nets != "" {
			t.Fatalf("networks remained: %q %v", nets, err)
		}
	}
	assertNoReal := func(root string) {
		t.Helper()
		err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), egressRealCredential) {
				return fmt.Errorf("real credential in %s", p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	filesDoc := `[{"filename":"store.go","patch":"@@ -1,2 +1,2 @@\n package store\n-func old() {}\n+func save() {}","additions":1,"deletions":1}]`
	writeInput := func(name string) string {
		t.Helper()
		input := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Join(input, "base"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(input, "AGENTS.md"), []byte("review"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(input, "files.json"), []byte(filesDoc), 0600); err != nil {
			t.Fatal(err)
		}
		return input
	}
	input := writeInput("clean-input")
	cleanOutput := filepath.Join(dir, "clean-output")
	r, err := runner.Run(ctx, sandbox.Request{Harness: c.Harnesses["mock"], Source: head, Base: base, Input: input, Output: cleanOutput, Role: "reviewer", Index: 0, Prompt: "review"})
	if err != nil {
		if b, readErr := os.ReadFile(filepath.Join(cleanOutput, "harness.log")); readErr == nil {
			t.Log(string(b))
		}
		if out, _ := dockerOutput(ctx, "logs", stubName); out != "" {
			t.Log("stub logs:", out)
		}
		t.Fatalf("clean egress run failed: %v", err)
	}
	if len(r.Findings) != 1 {
		t.Fatalf("unexpected report: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(cleanOutput, "report.json")); err != nil {
		t.Fatal("clean run did not write report.json")
	}
	stubLogs, err := dockerOutput(ctx, "logs", stubName)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stubLogs, "AUTH=Bearer "+egressRealCredential) {
		t.Fatalf("upstream never saw the real credential: %q", stubLogs)
	}
	if strings.Contains(stubLogs, "reviewd-sentinel-") {
		t.Fatalf("sentinel reached upstream: %q", stubLogs)
	}
	assertNoReal(cleanOutput)
	assertClean()
	leakHarness := c.Harnesses["mock"]
	leakHarness.Command = []string{"sh", "-c", `set -eu
reviewd agent init
reviewd agent finding --path store.go --line 2 --side RIGHT --priority 2 --confidence 0.98 --title 'Leak token' --body "token $HARNESS_AUTH" --evidence 'evidence'
cat >/tmp/overview.json <<'JSON'
{"summary":"Leak","confidence":2,"reasons":["Leak."],"important_files":[{"path":"store.go","description":"Leak."}],"sequence_diagram":"sequenceDiagram\n Caller->>Store: Save"}
JSON
reviewd agent overview --file /tmp/overview.json
reviewd agent submit
`, "mock", "{{.PromptFile}}"}
	leakInput := writeInput("leak-input")
	leakOutput := filepath.Join(dir, "leak-output")
	_, err = runner.Run(ctx, sandbox.Request{Harness: leakHarness, Source: head, Base: base, Input: leakInput, Output: leakOutput, Role: "reviewer", Index: 0, Prompt: "review"})
	if !errors.Is(err, sandbox.ErrEgressLeak) {
		t.Fatalf("leak not detected: %v", err)
	}
	if strings.Contains(err.Error(), "reviewd-sentinel-") {
		t.Fatalf("leak error exposes sentinel: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(leakOutput, "report.json")); !os.IsNotExist(statErr) {
		t.Fatal("report written despite leak")
	}
	if _, statErr := os.Stat(filepath.Join(leakOutput, "harness.log")); statErr != nil {
		t.Fatal("harness.log missing after leak")
	}
	assertNoReal(leakOutput)
	assertClean()
	sleepInput := writeInput("sleep-input")
	sleepHarness := c.Harnesses["mock"]
	sleepHarness.Command = []string{"sleep", "60"}
	short, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	if _, err := runner.Run(short, sandbox.Request{Harness: sleepHarness, Source: head, Base: base, Input: sleepInput, Output: filepath.Join(dir, "sleep-output"), Role: "reviewer", Index: 0}); err == nil {
		t.Fatal("timeout accepted")
	}
	assertClean()
	orphanProxy, err := dockerOutput(ctx, "run", "-d", "--label", "reviewd.managed=true", "--label", "reviewd.role=egress", "--label", "reviewd.owner="+runner.Owner, "alpine:3.21", "sleep", "60")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = dockerOutput(context.Background(), "rm", "-f", orphanProxy) })
	orphanNet := "reviewd-job-orphan" + suffix
	if out, err := dockerOutput(ctx, "network", "create", "--internal", "--label", "reviewd.managed=true", "--label", "reviewd.owner="+runner.Owner, orphanNet); err != nil {
		t.Fatalf("create orphan net: %s %v", out, err)
	}
	t.Cleanup(func() { _, _ = dockerOutput(context.Background(), "network", "rm", orphanNet) })
	if err := runner.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	assertClean()
}

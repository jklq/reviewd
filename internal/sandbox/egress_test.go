package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"reviewd/internal/config"
)

func egressPlanRequest() planRequest {
	return planRequest{
		harness:         config.Harness{Image: "harness:local", Egress: true, Credentials: []string{"shared"}, Env: []string{"STATIC_AUTH"}},
		egress:          &config.Egress{Image: "reviewd-egress:local", Command: []string{"/usr/local/bin/egress", "serve", "--listen", ":8080"}, Network: "bridge", CAFile: "/etc/reviewd/egress-ca.pem"},
		configFile:      "/etc/reviewd/reviewd.json",
		stateDirs:       []string{"/var/lib/reviewd/credentials/acct"},
		proxyAmbient:    []string{"HTTPS_PROXY"},
		args:            []string{"agent", "prompt"},
		uid:             "1000",
		gid:             "1000",
		harnessName:     "reviewd-abcdef",
		proxyName:       "reviewd-egress-123456",
		internalNetwork: "reviewd-job-789abc",
		memory:          "2g",
		cpus:            "2",
		owner:           "ownerhash",
		binary:          "/opt/reviewd/bin/reviewd",
		source:          "/data/head",
		base:            "/data/base",
		input:           "/data/input",
		values:          map[string]string{"HARNESS_AUTH": "real-secret-value"},
		sentinels:       map[string]string{"HARNESS_AUTH": "reviewd-sentinel-0123456789abcdef0123456789abcdef"},
		environ:         []string{"PATH=/usr/bin", "HOME=/root", "STATIC_AUTH=static-value", "HTTPS_PROXY=http://proxy:8080"},
	}
}

func envValue(argv []string, key string) (string, bool) {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] != "--env" {
			continue
		}
		name, value, found := strings.Cut(argv[i+1], "=")
		if name == key {
			return value, found
		}
	}
	return "", false
}

func TestPlanEgressHarness(t *testing.T) {
	p, err := planDockerRun(egressPlanRequest())
	if err != nil {
		t.Fatal(err)
	}
	network := ""
	for i := 0; i+1 < len(p.harnessArgv); i++ {
		if p.harnessArgv[i] == "--network" {
			network = p.harnessArgv[i+1]
		}
	}
	if network != "reviewd-job-789abc" {
		t.Fatalf("harness not on internal network: %s", network)
	}
	value, found := envValue(p.harnessArgv, "HARNESS_AUTH")
	if !found || value != "reviewd-sentinel-0123456789abcdef0123456789abcdef" {
		t.Fatalf("harness credential not a sentinel: %q %v", value, found)
	}
	for _, key := range []string{"HTTPS_PROXY", "HTTP_PROXY", "REVIEWD_EGRESS_CA"} {
		if _, found := envValue(p.harnessArgv, key); !found {
			t.Fatalf("missing harness proxy var %s", key)
		}
	}
	if value, found := envValue(p.harnessArgv, "HTTPS_PROXY"); !found || value != "http://reviewd-egress:8080" {
		t.Fatalf("bad HTTPS_PROXY: %q", value)
	}
	if value, found := envValue(p.harnessArgv, "REVIEWD_EGRESS_CA"); !found || value != "/review/egress-ca.pem" {
		t.Fatalf("bad REVIEWD_EGRESS_CA: %q", value)
	}
	if !slices.Contains(p.harnessArgv, "type=bind,src=/etc/reviewd/egress-ca.pem,dst=/review/egress-ca.pem,readonly") {
		t.Fatal("missing harness CA mount")
	}
	joined := strings.Join(p.harnessArgv, "\x00")
	if strings.Contains(joined, "real-secret-value") {
		t.Fatal("real credential value in harness argv")
	}
	joinedEnv := strings.Join(p.harnessEnv, "\x00")
	if strings.Contains(joinedEnv, "real-secret-value") {
		t.Fatal("real credential value in harness client env")
	}
	if !slices.Contains(p.harnessEnv, "STATIC_AUTH=static-value") {
		t.Fatal("static harness env lost")
	}
}

func TestPlanEgressProxy(t *testing.T) {
	p, err := planDockerRun(egressPlanRequest())
	if err != nil {
		t.Fatal(err)
	}
	if p.proxyArgv[0] != "run" || p.proxyArgv[1] != "-d" {
		t.Fatalf("proxy not detached: %v", p.proxyArgv[:2])
	}
	for _, want := range []string{"reviewd.role=egress", "reviewd-egress-123456"} {
		if !slices.Contains(p.proxyArgv, want) {
			t.Fatalf("missing proxy argv %s: %v", want, p.proxyArgv)
		}
	}
	if slices.Contains(p.proxyArgv, "--network-alias") {
		t.Fatalf("bridge network must skip alias: %v", p.proxyArgv)
	}
	custom := egressPlanRequest()
	custom.egress.Network = "reviewd-upstream"
	p, err = planDockerRun(custom)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--network-alias", "reviewd-egress"} {
		if !slices.Contains(p.proxyArgv, want) {
			t.Fatalf("missing proxy argv %s: %v", want, p.proxyArgv)
		}
	}
	p, err = planDockerRun(egressPlanRequest())
	if err != nil {
		t.Fatal(err)
	}
	network := ""
	for i := 0; i+1 < len(p.proxyArgv); i++ {
		if p.proxyArgv[i] == "--network" {
			network = p.proxyArgv[i+1]
		}
	}
	if network != "bridge" {
		t.Fatalf("proxy not on upstream network: %s", network)
	}
	if value, found := envValue(p.proxyArgv, "HARNESS_AUTH"); found || value != "" {
		t.Fatalf("proxy credential must be name-only: %q %v", value, found)
	}
	if !slices.Contains(p.proxyArgv, "HARNESS_AUTH") {
		t.Fatal("missing proxy credential export")
	}
	raw, found := envValue(p.proxyArgv, "REVIEWD_SENTINELS")
	if !found || !strings.Contains(raw, `"HARNESS_AUTH":"reviewd-sentinel-0123456789abcdef0123456789abcdef"`) {
		t.Fatalf("bad REVIEWD_SENTINELS: %q", raw)
	}
	for _, want := range []string{
		"type=bind,src=/etc/reviewd/reviewd.json,dst=/etc/reviewd/reviewd.json,readonly",
		"type=bind,src=/etc/reviewd/egress-ca.pem,dst=/etc/reviewd/egress-ca.pem,readonly",
		"type=bind,src=/var/lib/reviewd/credentials/acct,dst=/var/lib/reviewd/credentials/acct",
	} {
		if !slices.Contains(p.proxyArgv, want) {
			t.Fatalf("missing proxy mount %s", want)
		}
	}
	if !slices.Contains(p.proxyEnv, "HARNESS_AUTH=real-secret-value") {
		t.Fatal("real value missing from proxy client env")
	}
	if !slices.Contains(p.proxyArgv, "HTTPS_PROXY") {
		t.Fatal("ambient credential env not forwarded to proxy")
	}
	tail := p.proxyArgv[len(p.proxyArgv)-5:]
	want := []string{"reviewd-egress:local", "/usr/local/bin/egress", "serve", "--listen", ":8080"}
	if !slices.Equal(tail, want) {
		t.Fatalf("proxy image/command: %v", tail)
	}
}

func TestPlanDirectUnchanged(t *testing.T) {
	req := egressPlanRequest()
	req.harness.Egress = false
	req.harness.Network = "none"
	req.harness.Credentials = nil
	req.values = map[string]string{"HARNESS_AUTH": "real-secret-value"}
	req.sentinels = nil
	req.internalNetwork = ""
	req.proxyName = ""
	p, err := planDockerRun(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.proxyArgv) != 0 || len(p.proxyEnv) != 0 {
		t.Fatal("direct harness planned a proxy")
	}
	value, found := envValue(p.harnessArgv, "HARNESS_AUTH")
	if found || value != "" {
		t.Fatal("direct harness credential must be name-only")
	}
	if !slices.Contains(p.harnessArgv, "HARNESS_AUTH") {
		t.Fatal("missing direct credential export")
	}
	if _, found := envValue(p.harnessArgv, "HTTPS_PROXY"); found {
		t.Fatal("direct harness must not set proxy vars")
	}
	if !slices.Contains(p.harnessEnv, "HARNESS_AUTH=real-secret-value") {
		t.Fatal("direct client env lost real value")
	}
}

func TestPlanEgressErrors(t *testing.T) {
	base := egressPlanRequest()
	cases := map[string]func(*planRequest){
		"no block": func(r *planRequest) { r.egress = nil },
		"no ca": func(r *planRequest) {
			e := *r.egress
			e.CAFile = ""
			r.egress = &e
		},
		"no config":        func(r *planRequest) { r.configFile = "" },
		"no network":       func(r *planRequest) { r.internalNetwork = "" },
		"missing sentinel": func(r *planRequest) { r.sentinels = map[string]string{} },
		"missing static":   func(r *planRequest) { r.environ = []string{"PATH=/usr/bin"} },
	}
	for name, mutate := range cases {
		req := base
		mutate(&req)
		if _, err := planDockerRun(req); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestSentinelGeneration(t *testing.T) {
	first, err := newSentinels([]string{"A", "B"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := newSentinels([]string{"A", "B"})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, m := range []map[string]string{first, second} {
		if len(m) != 2 {
			t.Fatalf("wrong sentinel count: %v", m)
		}
		for _, v := range m {
			if !strings.HasPrefix(v, "reviewd-sentinel-") || len(v) != len("reviewd-sentinel-")+32 {
				t.Fatalf("bad sentinel format: %q", v)
			}
			for _, c := range v[len("reviewd-sentinel-"):] {
				if c != '-' && (c < '0' || c > '9') && (c < 'a' || c > 'f') {
					t.Fatalf("non-hex sentinel: %q", v)
				}
			}
			if seen[v] {
				t.Fatal("duplicate sentinel")
			}
			seen[v] = true
		}
	}
	if first["A"] == first["B"] {
		t.Fatal("exports share a sentinel")
	}
}

func TestCheckSentinels(t *testing.T) {
	dir := t.TempDir()
	sentinels := map[string]string{"HARNESS_AUTH": "reviewd-sentinel-aaaa"}
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	clean := write("clean.log", "harness output without secrets")
	if err := checkSentinels([]byte(`{"summary":"ok"}`), clean, sentinels, "reviewer", 0); err != nil {
		t.Fatalf("clean output flagged: %v", err)
	}
	if err := checkSentinels([]byte(`{"summary":"reviewd-sentinel-aaaa"}`), clean, sentinels, "reviewer", 1); !errors.Is(err, ErrEgressLeak) {
		t.Fatalf("transcript leak missed: %v", err)
	} else if strings.Contains(err.Error(), "reviewd-sentinel-aaaa") {
		t.Fatalf("leak error exposes sentinel: %v", err)
	}
	leaky := write("leaky.log", "token reviewd-sentinel-aaaa printed")
	err := checkSentinels([]byte(`{"summary":"ok"}`), leaky, sentinels, "validator", 0)
	if !errors.Is(err, ErrEgressLeak) {
		t.Fatalf("log leak missed: %v", err)
	}
	if !strings.Contains(err.Error(), "validator 0") {
		t.Fatalf("leak error missing role: %v", err)
	}
	if err := checkSentinels([]byte("reviewd-sentinel-aaaa"), clean, nil, "reviewer", 0); err != nil {
		t.Fatalf("disabled egress scanned: %v", err)
	}
	if err := checkSentinels([]byte("reviewd-sentinel-aaaa"), clean, map[string]string{}, "reviewer", 0); err != nil {
		t.Fatalf("empty sentinel set scanned: %v", err)
	}
}

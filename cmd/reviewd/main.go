package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"reviewd/internal/config"
	"reviewd/internal/credential"
	"reviewd/internal/github"
	"reviewd/internal/report"
	"reviewd/internal/review"
	"reviewd/internal/sandbox"
	"reviewd/internal/server"
	"reviewd/internal/store"
)

const help = `reviewd — self-hosted code review with bring-your-own harnesses

  reviewd init [--config reviewd.json] [--parallelism 3] [--image IMAGE]
               [--command '["harness","--prompt","{{.Prompt}}"]']
               [--harness-env CREDENTIAL_ENV] [--app-id ID]
  reviewd config check [--config reviewd.json]
  reviewd doctor [--config reviewd.json]
  reviewd serve [--config reviewd.json]
  reviewd review --repo OWNER/REPO --pr N --installation ID [--config ...]
  reviewd jobs list [--config ...]
  reviewd jobs show JOB_ID [--config ...]
  reviewd jobs retry JOB_ID [--config ...]
  reviewd jobs report JOB_ID [--config ...]
  reviewd agent help

Operator configuration is JSON. init writes defaults without overwriting files.
The agent CLI runs inside review containers and never contacts GitHub.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "reviewd:", err)
		os.Exit(1)
	}
}
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Print(help)
		return nil
	}
	if args[0] == "agent" {
		return agent(args[1:])
	}
	cmd := args[0]
	args = args[1:]
	sub := ""
	if cmd == "config" || cmd == "jobs" {
		if len(args) == 0 {
			return errors.New("subcommand required")
		}
		sub = args[0]
		args = args[1:]
	}
	// Accept job ID before flags, as shown in help, while retaining standard flag parsing.
	id := ""
	if cmd == "jobs" && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id = args[0]
		args = args[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	cfgPath := fs.String("config", "reviewd.json", "operator configuration")
	var parallel *int
	var image, command, env *string
	var appID *int64
	var repo *string
	var pr *int
	var installation *int64
	if cmd == "init" {
		defaults := config.Default()
		h := defaults.Harnesses[defaults.Reviewers[0]]
		parallel = fs.Int("parallelism", defaults.Parallelism, "parallel review containers, plus one validator")
		image = fs.String("image", h.Image, "default harness image")
		command = fs.String("command", "", "JSON command argument array")
		env = fs.String("harness-env", strings.Join(h.Env, ","), "comma-separated environment names for a custom harness")
		appID = fs.Int64("app-id", 0, "GitHub App ID")
	}
	if cmd == "review" {
		repo = fs.String("repo", "", "owner/repo")
		pr = fs.Int("pr", 0, "pull request number")
		installation = fs.Int64("installation", 0, "GitHub App installation ID")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		if cmd == "jobs" && id == "" && fs.NArg() == 1 {
			id = fs.Arg(0)
		} else {
			return errors.New("unexpected positional arguments")
		}
	}
	if cmd == "init" {
		return initConfig(*cfgPath, *parallel, *image, *command, *env, *appID)
	}
	c, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	switch cmd {
	case "config":
		return configCheck(sub)
	case "doctor":
		return doctor(c)
	case "serve":
		return serve(c)
	case "review":
		return manualReview(c, *repo, *pr, *installation)
	case "jobs":
		return jobsCommand(c, sub, id)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func initConfig(path string, parallel int, image, command, env string, appID int64) error {
	c := config.Default()
	c.Parallelism = parallel
	c.AppID = appID
	name := c.Reviewers[0]
	h := c.Harnesses[name]
	h.Image = image
	if command != "" {
		if err := json.Unmarshal([]byte(command), &h.Command); err != nil {
			return err
		}
		// A custom command owns its model selection; do not attribute the
		// default Codex model to it.
		h.Model = ""
	}
	h.Env = nil
	if env != "" {
		h.Env = strings.Split(env, ",")
		h.Credentials = nil
	}
	c.Harnesses[name] = h
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	fmt.Printf("Wrote %s. Configure your GitHub App key, webhook secret and harness credentials; see README.md.\n", path)
	return nil
}

func configCheck(sub string) error {
	if sub != "check" {
		return errors.New("use config check")
	}
	fmt.Println("Configuration valid.")
	return nil
}

func doctor(c config.Config) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	credentials := credential.New(c)
	if err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").Run(); err != nil {
		return fmt.Errorf("Docker unavailable: %w", err)
	}
	for name, h := range c.Harnesses {
		if err := exec.CommandContext(ctx, "docker", "image", "inspect", h.Image).Run(); err != nil {
			return fmt.Errorf("harness %s image unavailable: %w", name, err)
		}
		if strings.HasPrefix(h.Network, "reviewd-") {
			if err := exec.CommandContext(ctx, "docker", "network", "inspect", h.Network).Run(); err != nil {
				return fmt.Errorf("harness %s network %s unavailable: %w", name, h.Network, err)
			}
		}
		for _, key := range h.Env {
			if os.Getenv(key) == "" {
				return fmt.Errorf("missing %s for harness %s", key, name)
			}
		}
		if _, err := credentials.Environment(ctx, h.Credentials); err != nil {
			return err
		}
	}
	if len(os.Getenv(c.WebhookSecretEnv)) < 16 {
		return errors.New("webhook secret must contain at least 16 bytes")
	}
	app, err := github.NewApp(c.AppID, c.PrivateKeyFile)
	if err != nil {
		return err
	}
	if err = app.Verify(ctx); err != nil {
		return err
	}
	fmt.Println("Docker, harness images, environment and GitHub App authentication are ready.")
	return nil
}

func manualReview(c config.Config, repo string, pr int, installation int64) error {
	s, err := store.Open(c.DataDir)
	if err != nil {
		return err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	j, _, err := s.Enqueue("manual:"+hex.EncodeToString(nonce[:]), store.Job{Repo: repo, Number: pr, Installation: installation})
	if err != nil {
		return err
	}
	return printJSON(j)
}

func jobsCommand(c config.Config, sub, id string) error {
	s, err := store.Open(c.DataDir)
	if err != nil {
		return err
	}
	switch sub {
	case "list":
		j, err := s.List()
		if err != nil {
			return err
		}
		return printJSON(j)
	case "show":
		j, err := s.Get(id)
		if err != nil {
			return err
		}
		return printJSON(j)
	case "retry":
		return s.Retry(id)
	case "report":
		j, err := s.Get(id)
		if err != nil {
			return err
		}
		var r report.Report
		if err = readJSON(filepath.Join(s.RunDir(j.ID), "report.json"), &r); err != nil {
			return err
		}
		if err = r.Validate(); err != nil {
			return err
		}
		fmt.Print(r.Markdown(j.ID))
		return nil
	default:
		return errors.New("use jobs list/show/retry/report")
	}
}
func serve(c config.Config) error {
	secret := os.Getenv(c.WebhookSecretEnv)
	if len(secret) < 16 {
		return errors.New("webhook secret must contain at least 16 bytes")
	}
	s, err := store.Open(c.DataDir)
	if err != nil {
		return err
	}
	release, err := s.DaemonLock()
	if err != nil {
		return err
	}
	defer release()
	app, err := github.NewApp(c.AppID, c.PrivateKeyFile)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err = app.Verify(ctx); err != nil {
		return err
	}
	if err = s.Recover(); err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	if c.AgentBinary != "" {
		binary = c.AgentBinary
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return err
	}
	owner := sha256.Sum256([]byte(c.DataDir))
	runner := sandbox.Docker{Binary: binary, Memory: c.Memory, CPUs: c.CPUs, Owner: hex.EncodeToString(owner[:16]), Credentials: credential.New(c)}
	if err = runner.Cleanup(ctx); err != nil {
		return err
	}
	worker := server.Worker{Config: c, Store: s, App: app, Engine: review.Engine{Config: c, Runner: runner}}
	var wg sync.WaitGroup
	for i := 0; i < c.Workers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); worker.Loop(ctx) }()
	}
	mux := http.NewServeMux()
	mux.Handle("/webhooks/github", server.Webhook{Secret: []byte(secret), Store: s, AllowForks: c.AllowForks})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("ok\n")) })
	srv := &http.Server{Addr: c.Listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	stopped := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
		close(stopped)
	}()
	fmt.Fprintln(os.Stderr, "reviewd listening on", c.Listen)
	err = srv.ListenAndServe()
	stop()
	<-stopped
	wg.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

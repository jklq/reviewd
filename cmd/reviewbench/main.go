// reviewbench replays pinned snapshots without contacting or publishing to GitHub.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"reviewd/internal/benchmark"
	"reviewd/internal/config"
)

func main() {
	configPath := flag.String("config", "/etc/reviewd/reviewd.json", "existing harness configuration (read only)")
	input := flag.String("case", "", "prepared case directory with head, base, files.json, context.json")
	output := flag.String("output", "", "new output directory; must not exist")
	binary := flag.String("agent-binary", "bin/reviewd", "reporting binary mounted into sandbox")
	strategy := flag.String("strategy", "baseline", "baseline, single, preload, structural, targeted, sharded, bounded")
	steps := flag.Int("max-steps", 12, "model iterations for bounded strategy (1..64)")
	timeout := flag.Duration("timeout", 20*time.Minute, "whole replay deadline")
	adaptive := flag.Bool("adaptive-budget", true, "scale bounded steps/timeout by PR size (capped by --timeout)")
	flag.Parse()
	if *input == "" || *output == "" || *timeout <= 0 || *steps < 1 || *steps > 64 {
		flag.Usage()
		os.Exit(2)
	}
	c, err := config.Load(*configPath)
	if err != nil {
		fail(err)
	}
	c.AgentBinary, err = filepath.Abs(*binary)
	if err != nil {
		fail(err)
	}
	in, err := filepath.Abs(*input)
	if err != nil {
		fail(err)
	}
	out, err := filepath.Abs(*output)
	if err != nil {
		fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if err := benchmark.Run(ctx, c, in, out, *strategy, *timeout, *steps, *adaptive); err != nil {
		fail(err)
	}
	fmt.Println(out)
}
func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"reviewd/internal/credential"
)

const refreshLead = 90 * time.Second
const refreshRetry = time.Minute
const maxResultBytes = 1 << 20

type credItem struct {
	exports     []string
	expiresAt   time.Time
	hasExpiry   bool
	lastAttempt time.Time
}

type credentials struct {
	mu         sync.RWMutex
	sentinels  map[string]string
	reals      map[string]string
	items      map[string]*credItem
	configPath string
	reviewdBin string
}

func loadSentinels() (map[string]string, error) {
	raw := os.Getenv("REVIEWD_SENTINELS")
	if raw == "" {
		return nil, fmt.Errorf("REVIEWD_SENTINELS is required")
	}
	var sentinels map[string]string
	if err := json.Unmarshal([]byte(raw), &sentinels); err != nil {
		return nil, fmt.Errorf("invalid REVIEWD_SENTINELS: %w", err)
	}
	if sentinels == nil {
		sentinels = map[string]string{}
	}
	return sentinels, nil
}

func newCredentials(sentinels map[string]string, configPath, reviewdBin string) (*credentials, error) {
	c := &credentials{sentinels: sentinels, reals: map[string]string{}, items: map[string]*credItem{}, configPath: configPath, reviewdBin: reviewdBin}
	for export := range sentinels {
		value := os.Getenv(export)
		if value == "" {
			return nil, fmt.Errorf("credential export %s has no value", export)
		}
		c.reals[export] = value
	}
	if configPath == "" {
		return c, nil
	}
	byExport, err := configCredentialExports(configPath)
	if err != nil {
		return nil, err
	}
	for name, exports := range byExport {
		used := false
		for _, export := range exports {
			if _, ok := sentinels[export]; ok {
				used = true
				break
			}
		}
		if !used {
			continue
		}
		item := &credItem{exports: exports}
		c.items[name] = item
	}
	c.refreshDue(true)
	return c, nil
}

func configCredentialExports(path string) (map[string][]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Credentials map[string]struct {
			Exports []string `json:"exports"`
		} `json:"credentials"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for name, cred := range parsed.Credentials {
		out[name] = cred.Exports
	}
	return out, nil
}

func configCAFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Egress *struct {
			CAFile string `json:"ca_file"`
		} `json:"egress"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		return "", err
	}
	if parsed.Egress == nil {
		return "", nil
	}
	return parsed.Egress.CAFile, nil
}

func (c *credentials) requestPairs() []pair {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var pairs []pair
	for export, sentinel := range c.sentinels {
		real, ok := c.reals[export]
		if !ok || sentinel == "" || real == "" {
			continue
		}
		pairs = append(pairs, pair{old: sentinel, new: real})
	}
	return pairs
}

func (c *credentials) responsePairs() []pair {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var pairs []pair
	for export, sentinel := range c.sentinels {
		real, ok := c.reals[export]
		if !ok || sentinel == "" || real == "" {
			continue
		}
		pairs = append(pairs, pair{old: real, new: sentinel})
	}
	return pairs
}

func (c *credentials) maybeRefresh() {
	c.refreshDue(false)
}

func (c *credentials) refreshDue(startup bool) {
	if c.configPath == "" {
		return
	}
	now := time.Now()
	var due []string
	c.mu.RLock()
	for name, item := range c.items {
		if startup || !item.hasExpiry || now.Add(refreshLead).After(item.expiresAt) {
			if now.Sub(item.lastAttempt) >= refreshRetry || item.lastAttempt.IsZero() {
				due = append(due, name)
			}
		}
	}
	c.mu.RUnlock()
	for _, name := range due {
		c.mu.Lock()
		if item, ok := c.items[name]; ok {
			item.lastAttempt = now
		}
		c.mu.Unlock()
		c.refreshOne(name)
	}
}

func (c *credentials) refreshOne(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.reviewdBin, "credential", "env", "--config", c.configPath, name)
	cmd.Env = os.Environ()
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		stdLog.Printf("refresh %s failed", name)
		return
	}
	if stdout.Len() > maxResultBytes {
		stdLog.Printf("refresh %s failed", name)
		return
	}
	var result credential.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		stdLog.Printf("refresh %s failed", name)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.items[name]
	if !ok {
		return
	}
	for _, export := range item.exports {
		if value, ok := result.Env[export]; ok && value != "" {
			c.reals[export] = value
		}
	}
	item.expiresAt = result.ExpiresAt
	item.hasExpiry = true
	stdLog.Printf("refresh %s ok", name)
}

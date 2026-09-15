// Package github owns GitHub App authentication and the REST boundary.
package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jklq/reviewd/internal/report"
)

type App struct {
	ID      int64
	Slug    string
	Key     *rsa.PrivateKey
	BaseURL string
	HTTP    *http.Client
}

func NewApp(id int64, keyFile string) (*App, error) {
	if id <= 0 {
		return nil, errors.New("app_id must be positive")
	}
	b, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	p, _ := pem.Decode(b)
	if p == nil {
		return nil, errors.New("private key must be PEM")
	}
	k, err := x509.ParsePKCS1PrivateKey(p.Bytes)
	if err != nil {
		v, err := x509.ParsePKCS8PrivateKey(p.Bytes)
		if err != nil {
			return nil, err
		}
		var ok bool
		k, ok = v.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("private key must be RSA")
		}
	}
	return &App{ID: id, Key: k, BaseURL: "https://api.github.com", HTTP: &http.Client{Timeout: 2 * time.Minute}}, nil
}
func (a *App) JWT() (string, error) {
	enc := base64.RawURLEncoding.EncodeToString
	now := time.Now()
	p, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": a.ID})
	s := enc([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc(p)
	h := sha256.Sum256([]byte(s))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.Key, crypto.SHA256, h[:])
	return s + "." + enc(sig), err
}

type Client struct {
	App          *App
	Installation int64
	mu           sync.Mutex
	token        string
	expires      time.Time
}

func (a *App) Installation(id int64) *Client { return &Client{App: a, Installation: id} }
func (c *Client) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Until(c.expires) > 2*time.Minute {
		return c.token, nil
	}
	jwt, err := c.App.JWT()
	if err != nil {
		return "", err
	}
	var v struct {
		Token   string    `json:"token"`
		Expires time.Time `json:"expires_at"`
	}
	if err = c.App.request(ctx, jwt, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", c.Installation), map[string]any{}, &v); err != nil {
		return "", err
	}
	if v.Token == "" {
		return "", errors.New("empty installation token")
	}
	c.token = v.Token
	c.expires = v.Expires
	return c.token, nil
}

type APIError struct {
	Status       int
	Method, Path string
}

func (a *APIError) Error() string {
	return fmt.Sprintf("GitHub %s %s: HTTP %d", a.Method, a.Path, a.Status)
}
func (a *App) request(ctx context.Context, token, method, path string, in, out any) error {
	var body []byte
	var err error
	if in != nil {
		body, err = json.Marshal(in)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, a.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "reviewd")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{resp.StatusCode, method, path}
	}
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	t, err := c.Token(ctx)
	if err != nil {
		return err
	}
	return c.App.request(ctx, t, method, path, in, out)
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func ValidRepo(s string) bool { return repoPattern.MatchString(s) && !strings.Contains(s, "..") }
func ValidSHA(s string) bool  { return shaPattern.MatchString(s) }

type PR struct {
	Number       int    `json:"number"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	State        string `json:"state"`
	Draft        bool   `json:"draft"`
	ChangedFiles int    `json:"changed_files"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	Head         struct {
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

func (c *Client) Pull(ctx context.Context, repo string, n int) (PR, error) {
	var p PR
	err := c.Do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", repo, n), nil, &p)
	if err == nil && (!ValidSHA(p.Head.SHA) || !ValidSHA(p.Base.SHA)) {
		err = errors.New("invalid PR commit SHA")
	}
	return p, err
}
func (c *Client) Files(ctx context.Context, repo string, n int) ([]report.ChangedFile, error) {
	out := []report.ChangedFile{}
	for page := 1; page <= 30; page++ {
		var batch []report.ChangedFile
		err := c.Do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100&page=%d", repo, n, page), nil, &batch)
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if len(batch) < 100 {
			return out, nil
		}
	}
	return nil, errors.New("PR reaches GitHub's 3000-file limit; cannot guarantee full review")
}
func (c *Client) Comment(ctx context.Context, repo string, n int, body string) (int64, error) {
	var r struct {
		ID int64 `json:"id"`
	}
	err := c.Do(ctx, "POST", fmt.Sprintf("/repos/%s/issues/%d/comments", repo, n), map[string]string{"body": body}, &r)
	return r.ID, err
}
func (c *Client) EditComment(ctx context.Context, repo string, id int64, body string) error {
	return c.Do(ctx, "PATCH", fmt.Sprintf("/repos/%s/issues/comments/%d", repo, id), map[string]string{"body": body}, nil)
}
func (c *Client) Reaction(ctx context.Context, repo string, comment int64, content string) error {
	return c.Do(ctx, "POST", fmt.Sprintf("/repos/%s/issues/comments/%d/reactions", repo, comment), map[string]string{"content": content}, nil)
}

// IssueReaction reacts to the pull request/issue itself so automatic reviews
// show progress without a comment to anchor on.
func (c *Client) IssueReaction(ctx context.Context, repo string, number int, content string) error {
	return c.Do(ctx, "POST", fmt.Sprintf("/repos/%s/issues/%d/reactions", repo, number), map[string]string{"content": content}, nil)
}

// clearEyes relies on the idempotent create returning this installation's own
// reaction, so exactly that reaction is removed.
func (c *Client) clearEyes(ctx context.Context, path string) error {
	var mine struct {
		ID int64 `json:"id"`
	}
	if err := c.Do(ctx, "POST", path, map[string]string{"content": "eyes"}, &mine); err != nil {
		return err
	}
	return c.Do(ctx, "DELETE", fmt.Sprintf("%s/%d", path, mine.ID), nil, nil)
}
func (c *Client) ClearIssueEyes(ctx context.Context, repo string, number int) error {
	return c.clearEyes(ctx, fmt.Sprintf("/repos/%s/issues/%d/reactions", repo, number))
}
func (c *Client) ClearEyes(ctx context.Context, repo string, comment int64) error {
	return c.clearEyes(ctx, fmt.Sprintf("/repos/%s/issues/comments/%d/reactions", repo, comment))
}
func (c *Client) Review(ctx context.Context, repo string, n int, sha, body string, findings []report.Finding) error {
	comments := []map[string]any{}
	for _, f := range findings {
		m := map[string]any{"path": f.Path, "line": f.Line, "side": f.Side, "body": f.Markdown()}
		if f.StartLine > 0 && f.StartLine != f.Line {
			m["start_line"] = f.StartLine
			m["start_side"] = f.Side
		}
		comments = append(comments, m)
	}
	return c.Do(ctx, "POST", fmt.Sprintf("/repos/%s/pulls/%d/reviews", repo, n), map[string]any{"commit_id": sha, "event": "COMMENT", "body": body, "comments": comments}, nil)
}

// findInPages walks a paginated collection and reports whether any item matches.
func findInPages[T any](ctx context.Context, c *Client, path string, match func(T) bool) (bool, error) {
	for page := 1; ; page++ {
		var batch []T
		if err := c.Do(ctx, "GET", fmt.Sprintf("%s?per_page=100&page=%d", path, page), nil, &batch); err != nil {
			return false, err
		}
		for _, v := range batch {
			if match(v) {
				return true, nil
			}
		}
		if len(batch) < 100 {
			return false, nil
		}
	}
}

type reviewRef struct {
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (c *Client) bot() string { return c.App.Slug + "[bot]" }

func (c *Client) reviewsPath(repo string, n int) string {
	return fmt.Sprintf("/repos/%s/pulls/%d/reviews", repo, n)
}

// Publication uses a stable job marker to reconcile a lost response or restart.
func (c *Client) HasReview(ctx context.Context, repo string, n int, marker string) (bool, error) {
	return findInPages(ctx, c, c.reviewsPath(repo, n), func(r reviewRef) bool {
		return r.User.Login == c.bot() && strings.Contains(r.Body, "<!-- reviewd:"+marker+" -->")
	})
}

// HasPriorReview reports whether this App already published a review on the PR,
// which distinguishes a first review from a retrigger.
func (c *Client) HasPriorReview(ctx context.Context, repo string, n int) (bool, error) {
	return findInPages(ctx, c, c.reviewsPath(repo, n), func(r reviewRef) bool {
		return r.User.Login == c.bot() && strings.Contains(r.Body, "<!-- reviewd:")
	})
}

func (a *App) Verify(ctx context.Context) error {
	jwt, err := a.JWT()
	if err != nil {
		return err
	}
	var v struct {
		Slug string `json:"slug"`
	}
	if err = a.request(ctx, jwt, "GET", "/app", nil, &v); err != nil {
		return err
	}
	if v.Slug == "" {
		return errors.New("GitHub App has no slug")
	}
	a.Slug = v.Slug
	return nil
}
func (c *Client) FindComment(ctx context.Context, repo string, n int, marker string) (int64, error) {
	type commentRef struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	var found int64
	ok, err := findInPages(ctx, c, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, n), func(v commentRef) bool {
		if v.User.Login == c.bot() && strings.Contains(v.Body, marker) {
			found = v.ID
			return true
		}
		return false
	})
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	return found, nil
}

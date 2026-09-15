package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jklq/reviewd/internal/github"
	"github.com/jklq/reviewd/internal/store"
)

type Webhook struct {
	Secret     []byte
	Store      *store.Store
	AllowForks bool
}
type event struct {
	Action       string `json:"action"`
	Number       int    `json:"number"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Pull  github.PR `json:"pull_request"`
	Issue struct {
		Number int             `json:"number"`
		Pull   json.RawMessage `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		ID          int64  `json:"id"`
		Body        string `json:"body"`
		Association string `json:"author_association"`
		User        struct {
			Type string `json:"type"`
		} `json:"user"`
	} `json:"comment"`
}

func (w Webhook) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		rw.Header().Set("Allow", "POST")
		http.Error(rw, "POST required", 405)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, 2<<20))
	if err != nil {
		http.Error(rw, "payload too large", 413)
		return
	}
	sig := r.Header.Get("X-Hub-Signature-256")
	raw, err := hex.DecodeString(strings.TrimPrefix(sig, "sha256="))
	mac := hmac.New(sha256.New, w.Secret)
	mac.Write(body)
	if len(w.Secret) < 16 || !strings.HasPrefix(sig, "sha256=") || err != nil || !hmac.Equal(raw, mac.Sum(nil)) {
		http.Error(rw, "invalid signature", 401)
		return
	}
	if r.Header.Get("X-GitHub-Event") == "ping" {
		rw.WriteHeader(204)
		return
	}
	delivery := r.Header.Get("X-GitHub-Delivery")
	if delivery == "" || len(delivery) > 200 {
		http.Error(rw, "delivery ID required", 400)
		return
	}
	var v event
	if json.Unmarshal(body, &v) != nil {
		http.Error(rw, "invalid JSON", 400)
		return
	}
	j := store.Job{Repo: v.Repository.FullName, Installation: v.Installation.ID}
	switch r.Header.Get("X-GitHub-Event") {
	case "pull_request":
		if v.Action != "opened" && v.Action != "synchronize" && v.Action != "reopened" && v.Action != "ready_for_review" {
			rw.WriteHeader(204)
			return
		}
		if v.Pull.Draft || v.Pull.State != "open" || (!w.AllowForks && v.Pull.Head.Repo.FullName != v.Repository.FullName) {
			rw.WriteHeader(204)
			return
		}
		j.Number = v.Number
		j.Head = v.Pull.Head.SHA
	case "issue_comment":
		if v.Action != "created" || len(v.Issue.Pull) == 0 || string(v.Issue.Pull) == "null" || v.Comment.User.Type == "Bot" || !reviewCommand(v.Comment.Body) {
			rw.WriteHeader(204)
			return
		}
		switch v.Comment.Association {
		case "OWNER", "MEMBER", "COLLABORATOR":
		default:
			rw.WriteHeader(204)
			return
		}
		j.Number = v.Issue.Number
		j.TriggerComment = v.Comment.ID
	default:
		rw.WriteHeader(204)
		return
	}
	if !github.ValidRepo(j.Repo) || j.Number <= 0 || j.Installation <= 0 || (j.Head != "" && !github.ValidSHA(j.Head)) {
		http.Error(rw, "invalid target", 400)
		return
	}
	// Push events coalesce by head. A fresh lifecycle event must schedule another
	// review even when that head was previously superseded, failed or reviewed.
	// Its delivery ID still makes GitHub redelivery idempotent.
	key := "github:" + delivery
	if j.Head != "" && v.Action != "reopened" && v.Action != "ready_for_review" {
		key = "pull:" + j.Repo + ":" + j.Head + ":" + strconv.Itoa(j.Number)
	}
	job, created, err := w.Store.Enqueue(key, j)
	if err != nil {
		http.Error(rw, "could not persist job", 503)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(202)
	_ = json.NewEncoder(rw).Encode(map[string]any{"id": job.ID, "created": created})
}

// reviewMention matches the bare @reviewd handle as a standalone token, so
// ordinary prose like "seems fine\n\n@reviewd" still requests a review.
var reviewMention = regexp.MustCompile(`(?:^|[^\w@])@reviewd(?:[^\w]|$)`)

func reviewCommand(body string) bool {
	return reviewMention.MatchString(body)
}

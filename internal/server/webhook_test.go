package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"

	"reviewd/internal/store"
)

func TestSignedWebhookDurableAndDeduplicated(t *testing.T) {
	s, _ := store.Open(t.TempDir())
	secret := []byte("a-secret-at-least-sixteen-bytes")
	w := Webhook{Secret: secret, Store: s}
	body := `{"action":"opened","number":1,"installation":{"id":3},"repository":{"full_name":"o/r"},"pull_request":{"state":"open","head":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repo":{"full_name":"o/r"}}}}`
	send := func(signature bool, id string) int {
		r := httptest.NewRequest("POST", "/webhooks/github", strings.NewReader(body))
		r.Header.Set("X-GitHub-Event", "pull_request")
		r.Header.Set("X-GitHub-Delivery", id)
		if signature {
			m := hmac.New(sha256.New, secret)
			m.Write([]byte(body))
			r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(m.Sum(nil)))
		}
		rw := httptest.NewRecorder()
		w.ServeHTTP(rw, r)
		return rw.Code
	}
	if code := send(false, "d"); code != 401 {
		t.Fatal(code)
	}
	for _, id := range []string{"d", "d", "other"} {
		if code := send(true, id); code != 202 {
			t.Fatal(code)
		}
	}
	jobs, _ := s.List()
	if len(jobs) != 1 {
		t.Fatal(jobs)
	}
}
func TestCommentCommandIsExplicit(t *testing.T) {
	for _, body := range []string{"@reviewdx", "x@reviewd", "email foo@reviewd.com", "no mention here"} {
		if reviewCommand(body) {
			t.Fatal(body)
		}
	}
	for _, body := range []string{"@reviewd", " @reviewd\n", "hmm, seems fine\n\n@reviewd", "please @reviewd", "@reviewd please", "@reviewd review"} {
		if !reviewCommand(body) {
			t.Fatalf("command rejected: %q", body)
		}
	}
}

func TestLifecycleEventReviewsPreviouslySupersededHead(t *testing.T) {
	for _, action := range []string{"reopened", "ready_for_review"} {
		t.Run(action, func(t *testing.T) {
			s, _ := store.Open(t.TempDir())
			secret := []byte("a-secret-at-least-sixteen-bytes")
			w := Webhook{Secret: secret, Store: s}
			send := func(action, delivery string) {
				body := `{"action":"` + action + `","number":1,"installation":{"id":3},"repository":{"full_name":"o/r"},"pull_request":{"state":"open","head":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repo":{"full_name":"o/r"}}}}`
				r := httptest.NewRequest("POST", "/webhooks/github", strings.NewReader(body))
				r.Header.Set("X-GitHub-Event", "pull_request")
				r.Header.Set("X-GitHub-Delivery", delivery)
				m := hmac.New(sha256.New, secret)
				m.Write([]byte(body))
				r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(m.Sum(nil)))
				rw := httptest.NewRecorder()
				w.ServeHTTP(rw, r)
				if rw.Code != 202 {
					t.Fatal(rw.Code)
				}
			}
			send("opened", "first")
			j, ok, err := s.Claim()
			if err != nil || !ok {
				t.Fatal(err)
			}
			j.Status = "superseded"
			if err = s.Save(&j); err != nil {
				t.Fatal(err)
			}
			send(action, "second")
			send(action, "second")
			next, ok, err := s.Claim()
			if err != nil || !ok || next.ID == j.ID {
				t.Fatalf("no fresh review: %+v %v", next, err)
			}
			jobs, err := s.List()
			if err != nil || len(jobs) != 2 {
				t.Fatalf("redelivery duplicated: %+v %v", jobs, err)
			}
		})
	}
}

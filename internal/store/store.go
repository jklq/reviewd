// Package store is a single-host durable queue. Atomic rename + fsync makes
// acknowledged webhooks survive restarts; flock coordinates the daemon and CLI.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/jklq/reviewd/internal/github"
)

type Job struct {
	ID             string    `json:"id"`
	Repo           string    `json:"repo"`
	Number         int       `json:"number"`
	Installation   int64     `json:"installation"`
	Head           string    `json:"head,omitempty"`
	Base           string    `json:"base,omitempty"`
	TriggerComment int64     `json:"trigger_comment,omitempty"`
	Status         string    `json:"status"`
	Attempts       int       `json:"attempts"`
	Created        time.Time `json:"created"`
	Updated        time.Time `json:"updated"`
	Next           time.Time `json:"next"`
	Error          string    `json:"error,omitempty"`
	StatusComment  int64     `json:"status_comment,omitempty"`
	ReportReady    bool      `json:"report_ready"`
	Published      bool      `json:"published"`
}
type Store struct{ Dir string }

func Open(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for _, p := range []string{abs, filepath.Join(abs, "jobs"), filepath.Join(abs, "runs")} {
		if err = os.MkdirAll(p, 0700); err != nil {
			return nil, err
		}
	}
	return &Store{abs}, nil
}
func Atomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return Atomic(path, append(b, '\n'))
}
func (s *Store) lock(name string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(s.Dir, name), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func unlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
func (s *Store) DaemonLock() (func(), error) {
	f, err := os.OpenFile(filepath.Join(s.Dir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another reviewd daemon owns this data directory")
	}
	return func() { unlock(f) }, nil
}
func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
func (s *Store) path(id string) string { return filepath.Join(s.Dir, "jobs", id+".json") }
func (s *Store) Get(id string) (Job, error) {
	var j Job
	if !validID(id) {
		return j, errors.New("invalid job ID")
	}
	b, err := os.ReadFile(s.path(id))
	if err != nil {
		return j, err
	}
	err = json.Unmarshal(b, &j)
	return j, err
}
func (s *Store) List() ([]Job, error) {
	files, err := os.ReadDir(filepath.Join(s.Dir, "jobs"))
	if err != nil {
		return nil, err
	}
	jobs := []Job{}
	for _, f := range files {
		if filepath.Ext(f.Name()) != ".json" {
			continue
		}
		j, err := s.Get(f.Name()[:len(f.Name())-5])
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Created.Before(jobs[j].Created) })
	return jobs, nil
}
func (s *Store) Enqueue(key string, j Job) (Job, bool, error) {
	if !github.ValidRepo(j.Repo) || j.Number <= 0 || j.Installation <= 0 || (j.Head != "" && !github.ValidSHA(j.Head)) {
		return j, false, errors.New("invalid job target")
	}
	h := sha256.Sum256([]byte(key))
	j.ID = hex.EncodeToString(h[:16])
	f, err := s.lock("queue.lock")
	if err != nil {
		return j, false, err
	}
	defer unlock(f)
	old, err := s.Get(j.ID)
	if err == nil {
		return old, false, nil
	}
	if !os.IsNotExist(err) {
		return j, false, err
	}
	j.Status = "queued"
	j.Created = time.Now().UTC()
	j.Updated = j.Created
	j.Next = j.Created
	return j, true, WriteJSON(s.path(j.ID), j)
}
func (s *Store) Save(j *Job) error {
	if !validID(j.ID) {
		return errors.New("invalid job ID")
	}
	f, err := s.lock("queue.lock")
	if err != nil {
		return err
	}
	defer unlock(f)
	j.Updated = time.Now().UTC()
	return WriteJSON(s.path(j.ID), j)
}
func (s *Store) Claim() (Job, bool, error) {
	f, err := s.lock("queue.lock")
	if err != nil {
		return Job{}, false, err
	}
	defer unlock(f)
	jobs, err := s.List()
	if err != nil {
		return Job{}, false, err
	}
	busy := map[string]bool{}
	for _, j := range jobs {
		if j.Status == "running" {
			busy[fmt.Sprintf("%s#%d", j.Repo, j.Number)] = true
		}
	}
	for _, j := range jobs {
		if j.Status != "queued" || j.Next.After(time.Now()) || busy[fmt.Sprintf("%s#%d", j.Repo, j.Number)] {
			continue
		}
		j.Status = "running"
		j.Attempts++
		j.Updated = time.Now().UTC()
		return j, true, WriteJSON(s.path(j.ID), j)
	}
	return Job{}, false, nil
}
func (s *Store) Recover() error {
	f, err := s.lock("queue.lock")
	if err != nil {
		return err
	}
	defer unlock(f)
	jobs, err := s.List()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.Status == "running" {
			j.Status = "queued"
			j.Next = time.Now()
			if err = WriteJSON(s.path(j.ID), j); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Store) Retry(id string) error {
	f, err := s.lock("queue.lock")
	if err != nil {
		return err
	}
	defer unlock(f)
	j, err := s.Get(id)
	if err != nil {
		return err
	}
	if j.Status != "failed" {
		return errors.New("only failed jobs may be retried")
	}
	j.Status = "queued"
	j.Attempts = 0
	j.Error = ""
	j.Next = time.Now()
	return WriteJSON(s.path(id), j)
}
func (s *Store) RunDir(id string) string { return filepath.Join(s.Dir, "runs", id) }

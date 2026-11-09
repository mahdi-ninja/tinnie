// Package server is tinnie-server: a restic rest-server for several tinnie stacks, in append-only mode with a
// private repository per stack, plus the retention the stacks can no longer run themselves.
package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"tinnie/internal/config"
	"tinnie/internal/restic"
)

const (
	// Port is where rest-server listens inside the container.
	Port = "8000"
	// stateDir is the folder under the data dir that holds tinnie-server's own files. Stack names cannot start
	// with a dot, so it never collides with a repository.
	stateDir = ".tinnie-server"
)

type Server struct {
	Cfg  *config.Server
	Data string
}

func New(cfg *config.Server) *Server {
	data := os.Getenv("TINNIE_SERVER_DATA")
	if data == "" {
		data = "/data"
	}
	return &Server{Cfg: cfg, Data: filepath.Clean(data)}
}

func (s *Server) State() string    { return filepath.Join(s.Data, stateDir) }
func (s *Server) Htpasswd() string { return filepath.Join(s.State(), "htpasswd") }
func (s *Server) Cache() string    { return filepath.Join(s.State(), "cache") }
func (s *Server) status() string   { return filepath.Join(s.State(), "status.json") }
func (s *Server) lockFile() string { return filepath.Join(s.State(), "lock") }

// Repo opens a stack's repository with its backup password, seeing every host's snapshots.
func (s *Server) Repo(st config.Stack) restic.Repo {
	return restic.Repo{URL: st.Repository(s.Data), Password: st.BackupPassword, CacheDir: s.Cache()}
}

// HasRepository reports whether the stack has backed up at least once, so its repository exists.
func (s *Server) HasRepository(st config.Stack) bool {
	_, err := os.Stat(filepath.Join(st.Repository(s.Data), "config"))
	return err == nil
}

// Status is the outcome of the last retention run, read by the healthcheck and the console summary.
type Status struct {
	LastAttempt time.Time `json:"last_attempt"`
	LastResult  string    `json:"last_result"`
	LastError   string    `json:"last_error,omitempty"`
	LastSuccess time.Time `json:"last_success"`
}

func (s *Server) ReadStatus() (Status, bool) {
	var st Status
	data, err := os.ReadFile(s.status())
	if err != nil || json.Unmarshal(data, &st) != nil {
		return Status{}, false
	}
	return st, true
}

func (s *Server) WriteStatus(st Status) error {
	if err := os.MkdirAll(s.State(), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(st, "", "  ")
	tmp := s.status() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.status())
}

var ErrBusy = errors.New("another retention run is in progress in this container")

// Lock keeps scheduled and manual retention runs from overlapping.
func (s *Server) Lock() (func(), error) {
	if err := os.MkdirAll(s.State(), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.lockFile(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// DirSize adds up the files under dir; a missing dir is 0.
func DirSize(dir string) int64 {
	var total int64
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func (s *Server) formatTime(t time.Time) string {
	return t.In(s.Cfg.Retention.Schedule.Location()).Format("2006-01-02 15:04 MST")
}

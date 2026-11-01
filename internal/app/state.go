package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type Paths struct{ Root string }

func DefaultPaths() Paths {
	if root := os.Getenv("TINNIE_STATE_DIR"); root != "" {
		return Paths{Root: root}
	}
	return Paths{Root: "/var/lib/tinnie"}
}

func (p Paths) Stage() string    { return filepath.Join(p.Root, "stage") }
func (p Paths) Restore() string  { return filepath.Join(p.Root, "restore") }
func (p Paths) Cache() string    { return filepath.Join(p.Root, "cache") }
func (p Paths) Status() string   { return filepath.Join(p.Root, "status.json") }
func (p Paths) LockFile() string { return filepath.Join(p.Root, "lock") }
func (p Paths) Stopped() string  { return filepath.Join(p.Root, "stopped.json") }

type Status struct {
	LastAttempt  time.Time `json:"last_attempt"`
	LastResult   string    `json:"last_result"`
	LastError    string    `json:"last_error,omitempty"`
	LastSuccess  time.Time `json:"last_success"`
	LastSnapshot string    `json:"last_snapshot,omitempty"`
}

func (p Paths) ReadStatus() (Status, bool) {
	var st Status
	data, err := os.ReadFile(p.Status())
	if err != nil || json.Unmarshal(data, &st) != nil {
		return Status{}, false
	}
	return st, true
}

func (p Paths) WriteStatus(st Status) error {
	data, _ := json.MarshalIndent(st, "", "  ")
	tmp := p.Status() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.Status())
}

// stoppedContainer is a container tinnie stopped and still has to start again.
type stoppedContainer struct {
	Service string `json:"service"`
	ID      string `json:"id"`
	Name    string `json:"name"`
}

// ReadStopped returns the containers a backup or restore stopped and has not started again. The list
// outlives tinnie, so containers left stopped by a crash or a failed start are started on the next run.
func (p Paths) ReadStopped() ([]stoppedContainer, error) {
	data, err := os.ReadFile(p.Stopped())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var list []stoppedContainer
	if err == nil {
		err = json.Unmarshal(data, &list)
	}
	if err != nil {
		return nil, fmt.Errorf("could not read the list of services tinnie stopped (%s): %w", p.Stopped(), err)
	}
	return list, nil
}

// WriteStopped saves the list, or removes the file when the list is empty.
func (p Paths) WriteStopped(list []stoppedContainer) error {
	if len(list) == 0 {
		if err := os.Remove(p.Stopped()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(list, "", "  ")
	tmp := p.Stopped() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.Stopped())
}

var ErrBusy = errors.New("another backup or restore is running in this container")

// Lock holds an exclusive lock so scheduled backups, manual backups and restores never overlap.
func (p Paths) Lock() (func(), error) {
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p.LockFile(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("lock: %w", err)
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

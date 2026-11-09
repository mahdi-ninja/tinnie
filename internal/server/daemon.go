package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"tinnie/internal/config"
	"tinnie/internal/proc"
)

// Run writes the password file, starts rest-server, and runs retention on its schedule until ctx ends or
// rest-server exits.
func (s *Server) Run(ctx context.Context, version string) error {
	var names []string
	for _, st := range s.Cfg.Stacks {
		names = append(names, st.Name)
	}
	proc.Logf("tinnie-server %s serving %s in %s (append-only, private repositories), retention on %q (%s)", version, strings.Join(names, ", "), s.Data, s.Cfg.Retention.Cron, s.Cfg.Timezone)

	if err := os.MkdirAll(s.Data, 0o700); err != nil {
		return err
	}
	if err := WriteHtpasswd(s.Htpasswd(), s.Cfg.Stacks); err != nil {
		return err
	}
	exited, err := s.StartRestServer(ctx)
	if err != nil {
		return err
	}
	if err := waitForRestServer(ctx, exited); err != nil {
		return err
	}
	proc.Logf("rest-server listening on port %s", Port)

	for _, p := range s.CheckRepositories(ctx) {
		proc.Logf("warning: %s", p)
	}

	sched := s.Cfg.Retention.Schedule
	if prev := sched.Prev(time.Now()); !prev.IsZero() {
		if st, ok := s.ReadStatus(); ok && st.LastAttempt.Before(prev) {
			proc.Logf("missed the retention run due at %s; running now", s.formatTime(prev))
			s.scheduled(ctx)
		}
	}
	for {
		next := sched.Next(time.Now())
		proc.Logf("next retention run at %s", s.formatTime(next))
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			<-exited
			return nil
		case err := <-exited:
			timer.Stop()
			if err == nil {
				err = errors.New("exited")
			}
			return fmt.Errorf("rest-server %v", err)
		case <-timer.C:
		}
		s.scheduled(ctx)
	}
}

func (s *Server) scheduled(ctx context.Context) {
	err := s.Forget(ctx, nil)
	switch {
	case errors.Is(err, ErrBusy):
		proc.Logf("skipped: %v", err)
	case err != nil && ctx.Err() == nil:
		proc.Logf("retention failed: %v", err)
	}
}

// CheckRepositories opens every repository whose stack has a backup_password and reports the ones the password
// does not open, which usually means the two servers disagree about BACKUP_PASSWORD.
func (s *Server) CheckRepositories(ctx context.Context) []string {
	var problems []string
	for _, st := range s.Cfg.Stacks {
		if st.BackupPassword == "" || !s.HasRepository(st) {
			continue
		}
		if err := s.Repo(st).Check(ctx); err != nil {
			problems = append(problems, fmt.Sprintf("%s: backup_password does not open its repository: %v", st.Name, err))
		}
	}
	return problems
}

// Check validates the config, that rest-server answers, and that every backup_password opens its repository.
func (s *Server) Check(ctx context.Context, w func(string, ...any)) error {
	w("Config is valid. Checking:")
	failed := false
	if err := PingRestServer(ctx); err != nil {
		w("  ✗ rest-server: %v", err)
		failed = true
	} else {
		w("  ✓ rest-server on port %s", Port)
	}
	for _, st := range s.Cfg.Stacks {
		switch {
		case st.BackupPassword == "":
			w("  - %s: no backup_password, so no retention and no check", st.Name)
		case !s.HasRepository(st):
			w("  - %s: no backups yet", st.Name)
		default:
			if err := s.Repo(st).Check(ctx); err != nil {
				w("  ✗ %s: backup_password does not open the repository: %v", st.Name, err)
				failed = true
			} else {
				w("  ✓ %s: backup_password opens the repository", st.Name)
			}
		}
	}
	if failed {
		return errors.New("some checks failed")
	}
	return nil
}

// Restic runs any restic command against a stack's repository, with the password set, for the maintenance
// that clients cannot do over the append-only API.
func (s *Server) Restic(ctx context.Context, name string, args []string) error {
	st, ok := s.Cfg.Stack(name)
	if !ok {
		return fmt.Errorf("no stack named %q in the config", name)
	}
	if st.BackupPassword == "" {
		return fmt.Errorf("stack %s has no backup_password, so its repository cannot be opened", name)
	}
	return s.Repo(st).Exec(ctx, args)
}

// Stacks is a short description of each stack for the console.
func (s *Server) Stacks() []config.Stack { return s.Cfg.Stacks }

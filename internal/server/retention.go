package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"tinnie/internal/config"
	"tinnie/internal/notify"
	"tinnie/internal/proc"
)

// Forget applies each stack's keep rules to its repository and prunes. With names, only those stacks; otherwise
// every stack that has a backup_password. Stacks without a repository yet are skipped. The outcome is saved as
// the status the healthcheck reads, and notified.
func (s *Server) Forget(ctx context.Context, names []string) error {
	stacks, err := s.pick(names)
	if err != nil {
		return err
	}
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()

	started := time.Now()
	var failed []string
	var errs []error
	for _, st := range stacks {
		if st.BackupPassword == "" {
			if len(names) > 0 {
				return fmt.Errorf("stack %s has no backup_password, so its repository cannot be opened", st.Name)
			}
			continue
		}
		if !s.HasRepository(st) {
			proc.Logf("%s: no backups yet", st.Name)
			continue
		}
		proc.Logf("%s: removing snapshots outside the keep rules (%s)", st.Name, describeKeep(st.Keep))
		if err := s.Repo(st).Forget(ctx, st.Keep); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			proc.Logf("%s: retention failed: %v", st.Name, err)
			failed = append(failed, st.Name)
			errs = append(errs, fmt.Errorf("%s: %w", st.Name, err))
			continue
		}
		proc.Logf("%s: retention done", st.Name)
	}
	err = errors.Join(errs...)
	s.record(ctx, started, failed, err)
	return err
}

func (s *Server) pick(names []string) ([]config.Stack, error) {
	if len(names) == 0 {
		return s.Cfg.Stacks, nil
	}
	var out []config.Stack
	for _, n := range names {
		st, ok := s.Cfg.Stack(n)
		if !ok {
			return nil, fmt.Errorf("no stack named %q in the config", n)
		}
		out = append(out, st)
	}
	return out, nil
}

func (s *Server) record(ctx context.Context, started time.Time, failed []string, err error) {
	st, _ := s.ReadStatus()
	st.LastAttempt = started
	if err != nil {
		st.LastResult, st.LastError = "failure", err.Error()
	} else {
		st.LastResult, st.LastError, st.LastSuccess = "success", "", started
	}
	if werr := s.WriteStatus(st); werr != nil {
		proc.Logf("could not save status: %v", werr)
	}
	title := "tinnie-server: retention done"
	body := fmt.Sprintf("Retention ran on every stack.\nStarted: %s\nDuration: %s", started.UTC().Format(time.RFC3339), time.Since(started).Round(time.Second))
	if err != nil {
		title = "tinnie-server: retention failed for " + strings.Join(failed, ", ")
		body = fmt.Sprintf("Retention failed.\nStarted: %s\nDuration: %s\nError: %v", started.UTC().Format(time.RFC3339), time.Since(started).Round(time.Second), err)
	}
	notify.Send(ctx, s.Cfg.Notify, err != nil, title, body)
}

// Health is healthy when rest-server answers, the last retention run succeeded, and no more than one scheduled
// run has been missed. Before the first run it only checks rest-server.
func (s *Server) Health(ctx context.Context, now time.Time) error {
	if err := PingRestServer(ctx); err != nil {
		return err
	}
	st, ok := s.ReadStatus()
	if !ok {
		return nil
	}
	if st.LastResult != "success" {
		return fmt.Errorf("retention at %s failed: %s", s.formatTime(st.LastAttempt), st.LastError)
	}
	sched := s.Cfg.Retention.Schedule
	prev := sched.Prev(now)
	if prev.IsZero() {
		return nil
	}
	before := sched.Prev(prev.Add(-time.Second))
	if !before.IsZero() && st.LastSuccess.Before(before) {
		return fmt.Errorf("last successful retention was at %s; scheduled runs since then have not happened", s.formatTime(st.LastSuccess))
	}
	return nil
}

func describeKeep(k config.Keep) string {
	var parts []string
	add := func(name string, n int) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", name, n))
		}
	}
	if k.Within != "" {
		parts = append(parts, "within "+k.Within)
	}
	add("last", k.Last)
	add("hourly", k.Hourly)
	add("daily", k.Daily)
	add("weekly", k.Weekly)
	add("monthly", k.Monthly)
	add("yearly", k.Yearly)
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

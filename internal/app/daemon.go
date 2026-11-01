package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"tinnie/internal/proc"
	"tinnie/internal/restic"
)

const scheduledAttempts = 3

func (a *App) Run(ctx context.Context, version string) error {
	sched := a.Cfg.Schedule
	var names []string
	for _, s := range a.Sources {
		names = append(names, s.Name())
	}
	proc.Logf("tinnie %s backing up %s (%s) to %s on %q (%s)", version, a.Cfg.Name, strings.Join(names, ", "), restic.Redact(a.Cfg.Repository.URL), a.Cfg.Cron, a.Cfg.Timezone)
	a.startLeftoversOnStart(ctx)

	if prev := sched.Prev(time.Now()); !prev.IsZero() {
		latest, err := a.Repo.Latest(ctx)
		switch {
		case err != nil:
			proc.Logf("could not read snapshots (%v); backing up now", err)
			a.scheduled(ctx)
		case latest == nil:
			proc.Logf("no snapshots yet; backing up now")
			a.scheduled(ctx)
		case latest.Time.Before(prev):
			proc.Logf("missed the backup due at %s; backing up now", formatTime(prev, a))
			a.scheduled(ctx)
		}
	}

	for {
		next := sched.Next(time.Now())
		proc.Logf("next backup at %s", formatTime(next, a))
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		a.scheduled(ctx)
	}
}

// startLeftoversOnStart starts services a backup or restore left stopped before tinnie was last stopped or
// killed. Failing that is reported like a failed backup, so the healthcheck and notifications show it.
func (a *App) startLeftoversOnStart(ctx context.Context) {
	unlock, err := a.Paths.Lock()
	if err != nil {
		return // a backup or restore holds the lock, and starts them itself
	}
	defer unlock()
	started := time.Now()
	if err := a.startLeftovers(); err != nil {
		proc.Logf("%v", err)
		a.record(ctx, started, "", err)
	}
}

func (a *App) scheduled(ctx context.Context) {
	err := a.Backup(ctx, scheduledAttempts)
	switch {
	case errors.Is(err, ErrBusy):
		proc.Logf("skipped: %v", err)
	case err != nil && ctx.Err() == nil:
		proc.Logf("backup failed: %v", err)
	}
}

// Health is healthy when the last attempt succeeded and no more than one scheduled run has passed since.
func (a *App) Health(now time.Time) error {
	st, ok := a.Paths.ReadStatus()
	if !ok {
		return errors.New("no backup has finished yet")
	}
	if st.LastResult != "success" {
		return fmt.Errorf("last backup at %s failed: %s", formatTime(st.LastAttempt, a), st.LastError)
	}
	sched := a.Cfg.Schedule
	prev := sched.Prev(now)
	if prev.IsZero() {
		return nil
	}
	before := sched.Prev(prev.Add(-time.Second))
	if !before.IsZero() && st.LastSuccess.Before(before) {
		return fmt.Errorf("last successful backup was at %s; scheduled backups since then have not run", formatTime(st.LastSuccess, a))
	}
	return nil
}

func formatTime(t time.Time, a *App) string {
	return t.In(a.Cfg.Schedule.Location()).Format("2006-01-02 15:04 MST")
}

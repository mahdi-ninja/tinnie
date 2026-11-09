package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"tinnie/internal/config"
	notifypkg "tinnie/internal/notify"
)

// result is the outcome of one backup run, as told to notifications.
type result struct {
	Name     string
	Snapshot string
	Started  time.Time
	Duration time.Duration
	Err      error
}

func (r result) title() string {
	if r.Err != nil {
		return fmt.Sprintf("tinnie %s: backup failed", r.Name)
	}
	return fmt.Sprintf("tinnie %s: backup %s saved", r.Name, r.Snapshot)
}

func (r result) body() string {
	var b strings.Builder
	if r.Err != nil {
		fmt.Fprintf(&b, "The backup of %s failed.\n", r.Name)
	} else {
		fmt.Fprintf(&b, "The backup of %s was saved as snapshot %s.\n", r.Name, r.Snapshot)
	}
	fmt.Fprintf(&b, "Started: %s\n", r.Started.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Duration: %s", r.Duration.Round(time.Second))
	if r.Err != nil {
		fmt.Fprintf(&b, "\nError: %v", r.Err)
	}
	return b.String()
}

// notify sends the result to every notify URL: failures always, successes only with level info.
func notify(ctx context.Context, n config.Notify, r result) {
	notifypkg.Send(ctx, n, r.Err != nil, r.title(), r.body())
}

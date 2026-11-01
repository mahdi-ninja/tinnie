package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nicholas-fedor/shoutrrr/pkg/router"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"

	"tinnie/internal/config"
	"tinnie/internal/proc"
)

const notifyTimeout = 15 * time.Second

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
// Each URL gets its own 15 second budget; a failed notification is logged and never fails the backup.
func notify(ctx context.Context, n config.Notify, r result) {
	if len(n.URLs) == 0 || (r.Err == nil && n.Level != config.NotifyInfo) {
		return
	}
	sender, err := router.NewWithOptions(nil, types.SenderOptions{Timeout: notifyTimeout}, n.URLs...)
	if err != nil {
		proc.Logf("notify: %v", err)
		return
	}
	defer sender.Close()
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	params := types.Params{}
	params.SetTitle(r.title())
	for i, err := range sender.SendContext(ctx, r.body(), &params) {
		if err != nil {
			proc.Logf("notify: notify.urls[%d] (%s): %v", i, scheme(n.URLs[i]), err)
		}
	}
}

func scheme(u string) string {
	if i := strings.Index(u, ":"); i > 0 {
		return u[:i]
	}
	return "?"
}

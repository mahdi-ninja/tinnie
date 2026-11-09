// Package notify sends shoutrrr notifications the way tinnie and tinnie-server both do: failures always,
// successes only with level info, and never failing the job over a failed notification.
package notify

import (
	"context"
	"strings"
	"time"

	"github.com/nicholas-fedor/shoutrrr/pkg/router"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"

	"tinnie/internal/config"
	"tinnie/internal/proc"
)

const timeout = 15 * time.Second

// Send delivers title and body to every URL. A success (failed false) is sent only when the level is info.
// Each URL gets its own 15 second budget, and failures are logged.
func Send(ctx context.Context, n config.Notify, failed bool, title, body string) {
	if len(n.URLs) == 0 || (!failed && n.Level != config.NotifyInfo) {
		return
	}
	sender, err := router.NewWithOptions(nil, types.SenderOptions{Timeout: timeout}, n.URLs...)
	if err != nil {
		proc.Logf("notify: %v", err)
		return
	}
	defer sender.Close()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	params := types.Params{}
	params.SetTitle(title)
	for i, err := range sender.SendContext(ctx, body, &params) {
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

package app

import (
	"fmt"
	"io"
	"time"

	"tinnie/internal/restic"
)

const Commands = `Commands:
  tinnie snapshots                        list snapshots
  tinnie backup                           back up every source now
  tinnie restore [snapshot [source...]]   replace sources with a snapshot (default: latest, every source)
  tinnie check                            validate the config and test every connection
  tinnie help                             show this help
`

func (a *App) PrintSummary(w io.Writer) {
	fmt.Fprintf(w, "tinnie backs up %q on %q (%s). Next backup: %s\n", a.Cfg.Name, a.Cfg.Cron, a.Cfg.Timezone, formatTime(a.Cfg.Schedule.Next(time.Now()), a))
	fmt.Fprintln(w, "\nSources:")
	for _, s := range a.Sources {
		fmt.Fprintf(w, "  %-12s %-9s %s\n", s.Name(), s.Type(), a.describe(s))
	}
	fmt.Fprintf(w, "\nRepository: %s\n", restic.Redact(a.Cfg.Repository.URL))
	if st, ok := a.Paths.ReadStatus(); ok {
		if st.LastResult == "success" {
			fmt.Fprintf(w, "Last backup: %s, snapshot %s\n", formatTime(st.LastSuccess, a), st.LastSnapshot)
		} else {
			fmt.Fprintf(w, "Last backup: FAILED at %s: %s\n", formatTime(st.LastAttempt, a), st.LastError)
			if !st.LastSuccess.IsZero() {
				fmt.Fprintf(w, "Last success: %s, snapshot %s\n", formatTime(st.LastSuccess, a), st.LastSnapshot)
			}
		}
	} else {
		fmt.Fprintln(w, "Last backup: none yet")
	}
	fmt.Fprintln(w)
}

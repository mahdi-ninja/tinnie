package server

import (
	"context"
	"fmt"
	"io"
	"time"
)

const Commands = `Commands:
  tinnie-server forget [stack...]         run retention now (default: every stack with a backup_password)
  tinnie-server snapshots <stack>         list a stack's snapshots
  tinnie-server check                     validate the config, rest-server and every backup_password
  tinnie-server restic <stack> <args...>  run any restic command on a stack's repository
  tinnie-server help                      show this help
`

func (s *Server) PrintSummary(ctx context.Context, w io.Writer) {
	fmt.Fprintf(w, "tinnie-server keeps backups in %s (append-only). Retention on %q (%s), next at %s\n", s.Data, s.Cfg.Retention.Cron, s.Cfg.Timezone, s.formatTime(s.Cfg.Retention.Schedule.Next(time.Now())))
	if s.Cfg.MaxSizeText != "" {
		fmt.Fprintf(w, "Size limit: %s\n", s.Cfg.MaxSizeText)
	}
	fmt.Fprintln(w, "\nStacks:")
	for _, st := range s.Cfg.Stacks {
		fmt.Fprintf(w, "  %s\n", st.Name)
		fmt.Fprintf(w, "    url:       %s\n", st.ClientURL(s.Cfg.URL))
		switch {
		case !s.HasRepository(st):
			fmt.Fprintf(w, "    backups:   none yet\n")
		case st.BackupPassword == "":
			fmt.Fprintf(w, "    backups:   %s on disk; no backup_password, so snapshots are unknown here\n", humanBytes(DirSize(st.Repository(s.Data))))
		default:
			size := humanBytes(DirSize(st.Repository(s.Data)))
			if latest, err := s.Repo(st).Latest(ctx); err != nil {
				fmt.Fprintf(w, "    backups:   %s on disk; cannot read snapshots: %v\n", size, err)
			} else if latest == nil {
				fmt.Fprintf(w, "    backups:   %s on disk, no snapshots\n", size)
			} else {
				fmt.Fprintf(w, "    backups:   %s on disk, last snapshot %s at %s\n", size, latest.ShortID, s.formatTime(latest.Time))
			}
		}
		if st.BackupPassword == "" {
			fmt.Fprintf(w, "    retention: none (no backup_password)\n")
		} else {
			fmt.Fprintf(w, "    retention: keep %s\n", describeKeep(st.Keep))
		}
	}
	if st, ok := s.ReadStatus(); ok {
		if st.LastResult == "success" {
			fmt.Fprintf(w, "\nLast retention: %s\n", s.formatTime(st.LastSuccess))
		} else {
			fmt.Fprintf(w, "\nLast retention: FAILED at %s: %s\n", s.formatTime(st.LastAttempt), st.LastError)
		}
	} else {
		fmt.Fprintln(w, "\nLast retention: none yet")
	}
	fmt.Fprintln(w)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

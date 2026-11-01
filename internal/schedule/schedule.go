package schedule

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

type Schedule struct {
	spec cron.Schedule
	loc  *time.Location
}

func Parse(expr string, loc *time.Location) (*Schedule, error) {
	if strings.HasPrefix(strings.TrimSpace(expr), "@every") {
		return nil, fmt.Errorf("@every is not supported; use a cron expression such as \"0 */6 * * *\"")
	}
	spec, err := cron.ParseStandard(expr)
	if err != nil {
		return nil, err
	}
	return &Schedule{spec: spec, loc: loc}, nil
}

func (s *Schedule) Location() *time.Location { return s.loc }

func (s *Schedule) Next(t time.Time) time.Time { return s.spec.Next(t.In(s.loc)) }

// Prev returns the latest run time at or before t, or the zero time if none falls within five years.
func (s *Schedule) Prev(t time.Time) time.Time {
	t = t.Truncate(time.Second)
	for _, back := range []time.Duration{time.Hour, 25 * time.Hour, 32 * 24 * time.Hour, 367 * 24 * time.Hour, 5 * 367 * 24 * time.Hour} {
		cur := s.Next(t.Add(-back))
		if cur.After(t) {
			continue
		}
		for {
			n := s.Next(cur)
			if n.After(t) {
				return cur
			}
			cur = n
		}
	}
	return time.Time{}
}

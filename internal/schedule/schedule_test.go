package schedule

import (
	"testing"
	"time"
)

func TestPrevAndNext(t *testing.T) {
	syd, _ := time.LoadLocation("Australia/Sydney")
	s, err := Parse("0 */6 * * *", syd)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 13, 30, 0, 0, syd)
	if got, want := s.Prev(now), time.Date(2026, 11, 1, 12, 0, 0, 0, syd); !got.Equal(want) {
		t.Errorf("Prev = %v, want %v", got, want)
	}
	if got, want := s.Next(now), time.Date(2026, 11, 1, 18, 0, 0, 0, syd); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
	exact := time.Date(2026, 11, 1, 18, 0, 0, 0, syd)
	if got := s.Prev(exact); !got.Equal(exact) {
		t.Errorf("Prev at a run time = %v, want %v", got, exact)
	}
}

func TestPrevForRareSchedules(t *testing.T) {
	s, err := Parse("0 3 1 1 *", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if got, want := s.Prev(now), time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Prev = %v, want %v", got, want)
	}
}

func TestRejectsEvery(t *testing.T) {
	if _, err := Parse("@every 6h", time.UTC); err == nil {
		t.Error("expected @every to be rejected")
	}
	if _, err := Parse("@daily", time.UTC); err != nil {
		t.Errorf("@daily: %v", err)
	}
}

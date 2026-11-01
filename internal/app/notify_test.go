package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tinnie/internal/config"
)

func TestNotify(t *testing.T) {
	got := make(chan map[string]string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]string
		if err := json.Unmarshal(body, &m); err != nil {
			t.Errorf("body %s: %v", body, err)
		}
		got <- m
	}))
	defer srv.Close()
	url := "generic://" + strings.TrimPrefix(srv.URL, "http://") + "/notify?disabletls=yes&template=json"
	started := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)

	failed := result{Name: "myapp", Started: started, Duration: 12 * time.Second, Err: errors.New("db: pg_dump: exit status 1")}
	saved := result{Name: "myapp", Snapshot: "4f2a91c3", Started: started, Duration: 12 * time.Second}

	notify(context.Background(), config.Notify{URLs: []string{url}, Level: config.NotifyError}, saved)
	notify(context.Background(), config.Notify{URLs: []string{url}, Level: config.NotifyError}, failed)
	m := <-got
	if m["title"] != "tinnie myapp: backup failed" || !strings.Contains(m["message"], "Error: db: pg_dump: exit status 1") || !strings.Contains(m["message"], "Started: 2026-11-01T06:00:00Z") {
		t.Errorf("failure = %+v", m)
	}

	notify(context.Background(), config.Notify{URLs: []string{url}, Level: config.NotifyInfo}, saved)
	m = <-got
	if m["title"] != "tinnie myapp: backup 4f2a91c3 saved" || !strings.Contains(m["message"], "Duration: 12s") {
		t.Errorf("success = %+v", m)
	}
	select {
	case m := <-got:
		t.Errorf("unexpected notification %+v", m)
	default:
	}
}

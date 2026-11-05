package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveTokenKeepsOnePerServerAndGroup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HUDDLE_CONFIG_DIR", dir)
	t.Setenv("HUDDLE_TOKEN", "")

	for _, s := range []struct{ url, group, token string }{
		{"https://a.example/", "jobb", "hdl_hk_1"},
		{"https://a.example", "privat", "hdl_hk_2"},
		{"https://a.example", "jobb", "hdl_hk_3"}, // replaces hdl_hk_1
	} {
		if err := saveToken(s.url, s.group, s.token); err != nil {
			t.Fatal(err)
		}
	}
	ts := hookTokens()
	if len(ts) != 2 || ts[0].Token != "hdl_hk_2" || ts[1].Token != "hdl_hk_3" || ts[1].URL != "https://a.example" {
		t.Fatalf("got %+v", ts)
	}
	if fi, err := os.Stat(filepath.Join(dir, "hook-tokens.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file should be private: %v %v", fi.Mode(), err)
	}

	if err := saveToken("https://a.example", "privat", ""); err != nil {
		t.Fatal(err)
	}
	if ts := hookTokens(); len(ts) != 1 || ts[0].Group != "jobb" {
		t.Fatalf("after remove got %+v", ts)
	}

	// HUDDLE_TOKEN still wins, for hooks set up the old way.
	t.Setenv("HUDDLE_TOKEN", "hdl_hk_env")
	t.Setenv("HUDDLE_URL", "https://b.example/")
	if ts := hookTokens(); len(ts) != 1 || ts[0].Token != "hdl_hk_env" || ts[0].URL != "https://b.example" {
		t.Fatalf("env token: got %+v", ts)
	}
}

func TestReadTokenTrims(t *testing.T) {
	if got, err := readToken(strings.NewReader("  hdl_hk_x \n")); err != nil || got != "hdl_hk_x" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// doorbells serves /channel/events and /hook/inbox for one token; ring says
// whether it has an event.
func doorbells(t *testing.T, token string, ring bool) *httptest.Server {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/hook/inbox" {
			_, _ = io.WriteString(w, "[huddle] news for "+token+"\n")
			return
		}
		if !ring || r.URL.Query().Get("after") == "1" {
			select { // a long-poll with nothing new
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
			_, _ = io.WriteString(w, `{"events":[],"last_id":1}`)
			return
		}
		_, _ = io.WriteString(w, `{"events":[{"id":1,"kind":"question_asked","text":"question for `+token+`"}],"last_id":1}`)
	}))
	t.Cleanup(hs.Close)
	return hs
}

func TestRewakeAllWakesOnAnySavedToken(t *testing.T) {
	quiet := doorbells(t, "hdl_hk_quiet", false)
	loud := doorbells(t, "hdl_hk_loud", true)
	ts := []hookToken{
		{URL: quiet.URL, Group: "privat", Token: "hdl_hk_quiet"},
		{URL: loud.URL, Group: "jobb", Token: "hdl_hk_loud"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out strings.Builder
	start := time.Now()
	code := rewakeAll(ctx, ts, t.TempDir(), slog.New(slog.DiscardHandler), &out)
	if code != 2 || !strings.Contains(out.String(), "question for hdl_hk_loud") || strings.Contains(out.String(), "hdl_hk_quiet") {
		t.Fatalf("want a wake for the loud token only, got %d: %q", code, out.String())
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the quiet poller should stop as soon as the other one rings")
	}
}

func TestInboxHookPrintsEverySavedToken(t *testing.T) {
	t.Setenv("HUDDLE_CONFIG_DIR", t.TempDir())
	t.Setenv("HUDDLE_TOKEN", "")
	a := doorbells(t, "hdl_hk_a", false)
	b := doorbells(t, "hdl_hk_b", false)
	for i, s := range []*httptest.Server{a, b} {
		if err := saveToken(s.URL, []string{"x", "y"}[i], []string{"hdl_hk_a", "hdl_hk_b"}[i]); err != nil {
			t.Fatal(err)
		}
	}
	var out strings.Builder
	inboxHook(context.Background(), &out)
	if !strings.Contains(out.String(), "news for hdl_hk_a") || !strings.Contains(out.String(), "news for hdl_hk_b") {
		t.Fatalf("got %q", out.String())
	}
}

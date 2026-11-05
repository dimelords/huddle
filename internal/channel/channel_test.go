package channel

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDoorbellBridge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	afters := make(chan string, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		after := r.URL.Query().Get("after")
		afters <- after
		if after != "" { // nothing new: hold the long-poll
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
			_, _ = io.WriteString(w, `{"events":[],"last_id":5}`)
			return
		}
		_, _ = io.WriteString(w, `{"events":[{"id":5,"kind":"question_asked","actor":"alice","ref_id":2,"text":"alice sent you question #2."}],"last_id":5}`)
	}))
	defer srv.Close()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{BaseURL: srv.URL, Token: "tok", Version: "test", Log: slog.New(slog.DiscardHandler), Wait: 1}, inR, outW)
	}()
	out := bufio.NewScanner(outR)
	read := func() map[string]any {
		t.Helper()
		if !out.Scan() {
			t.Fatalf("no output: %v", out.Err())
		}
		var m map[string]any
		if err := json.Unmarshal(out.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	write := func(s string) {
		t.Helper()
		if _, err := io.WriteString(inW, s+"\n"); err != nil {
			t.Fatal(err)
		}
	}

	// A revision Claude Code would not register as a channel falls back.
	write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28"}}`)
	res := read()["result"].(map[string]any)
	if res["protocolVersion"] != fallbackVersion {
		t.Fatalf("protocolVersion = %v", res["protocolVersion"])
	}
	exp := res["capabilities"].(map[string]any)["experimental"].(map[string]any)
	if _, ok := exp["claude/channel"]; !ok {
		t.Fatalf("missing claude/channel capability: %v", res)
	}

	write(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	n := read()
	if n["method"] != "notifications/claude/channel" {
		t.Fatalf("want channel notification, got %v", n)
	}
	p := n["params"].(map[string]any)
	meta := p["meta"].(map[string]any)
	if p["content"] != "alice sent you question #2." || meta["kind"] != "question_asked" || meta["ref_id"] != "2" || meta["from"] != "alice" {
		t.Fatalf("notification params: %v", p)
	}

	// The next poll continues after the last delivered event.
	if first, next := <-afters, <-afters; first != "" || next != "5" {
		t.Fatalf("after params: %q then %q", first, next)
	}

	write(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if m := read(); m["id"] != float64(2) || m["error"] != nil {
		t.Fatalf("ping: %v", m)
	}
	write(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	if m := read(); m["error"] == nil {
		t.Fatalf("unknown method should error: %v", m)
	}

	_ = inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Run did not return after stdin closed")
	}
}

func TestRewake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("after") == "7" { // already delivered: nothing new
			_, _ = io.WriteString(w, `{"events":[],"last_id":7}`)
			return
		}
		_, _ = io.WriteString(w, `{"events":[{"id":7,"kind":"question_asked","actor":"alice","ref_id":3,"text":"alice sent you question #3."}],"last_id":7}`)
	}))
	defer srv.Close()
	dir := t.TempDir()
	cfg := func(token string) Config {
		return Config{BaseURL: srv.URL, Token: token, Log: slog.New(slog.DiscardHandler), Wait: 1}
	}

	var stderr strings.Builder
	if code := Rewake(ctx, cfg("tok"), dir, &stderr); code != 2 || !strings.Contains(stderr.String(), "alice sent you question #3.") {
		t.Fatalf("want exit 2 with doorbell, got %d: %q", code, stderr.String())
	}

	// The position is remembered, so the same doorbell does not ring twice.
	short, stop := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer stop()
	stderr.Reset()
	if code := Rewake(short, cfg("tok"), dir, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("second run should find nothing, got %d: %q", code, stderr.String())
	}

	if code := Rewake(ctx, cfg("bad"), dir, &stderr); code != 0 {
		t.Fatalf("rejected token should exit 0, got %d", code)
	}
}

func TestRunForwardsEverySource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server := func(token, text string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("after") == "1" {
				<-r.Context().Done() // nothing more
				return
			}
			_, _ = io.WriteString(w, `{"events":[{"id":1,"kind":"question_asked","text":"`+text+`"}],"last_id":1}`)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	jobb, privat := server("hdl_hk_j", "from jobb"), server("hdl_hk_p", "from privat")

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		_ = Run(ctx, Config{Sources: []Source{{jobb.URL, "hdl_hk_j"}, {privat.URL, "hdl_hk_p"}},
			Log: slog.New(slog.DiscardHandler), Wait: 1}, inR, outW)
	}()
	defer inW.Close()
	out := bufio.NewScanner(outR)
	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n")
	out.Scan() // the initialize result
	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
	got := map[string]bool{}
	for range 2 {
		if !out.Scan() {
			t.Fatalf("no notification: %v", out.Err())
		}
		var m struct {
			Params struct {
				Content string `json:"content"`
			} `json:"params"`
		}
		if err := json.Unmarshal(out.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		got[m.Params.Content] = true
	}
	if !got["from jobb"] || !got["from privat"] {
		t.Fatalf("want a doorbell from each source, got %v", got)
	}
}

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dimelords/huddle/internal/channel"
)

// hookToken is a saved hook token: the hooks (rewake, inbox-hook) read these
// so nobody has to export HUDDLE_TOKEN or edit settings.json.
type hookToken struct {
	URL   string `json:"url"`
	Group string `json:"group,omitempty"`
	Token string `json:"token"`
}

// configDir holds the saved hook tokens: HUDDLE_CONFIG_DIR, or
// huddle-mcp under the user's config directory.
func configDir() (string, error) {
	if d := os.Getenv("HUDDLE_CONFIG_DIR"); d != "" {
		return d, nil
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "huddle-mcp"), nil
}

func tokensPath() (string, error) {
	d, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "hook-tokens.json"), nil
}

func loadHookTokens() ([]hookToken, error) {
	p, err := tokensPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ts []hookToken
	if err := json.Unmarshal(b, &ts); err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	return ts, nil
}

func storeHookTokens(ts []hookToken) error {
	p, err := tokensPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// saveToken keeps one token per server and group: a new one replaces the
// old, and an empty token removes it.
func saveToken(url, group, token string) error {
	url = strings.TrimRight(url, "/")
	ts, err := loadHookTokens()
	if err != nil {
		return err
	}
	out := ts[:0]
	for _, t := range ts {
		if t.URL != url || t.Group != group {
			out = append(out, t)
		}
	}
	if token != "" {
		out = append(out, hookToken{URL: url, Group: group, Token: token})
	}
	return storeHookTokens(out)
}

// readToken reads the token from stdin, so it never shows up in ps or in
// the shell history.
func readToken(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// hookTokens are the tokens the hooks use: HUDDLE_TOKEN when set (the old
// way), otherwise the saved ones.
func hookTokens() []hookToken {
	if t := os.Getenv("HUDDLE_TOKEN"); t != "" {
		return []hookToken{{URL: strings.TrimRight(env("HUDDLE_URL", "http://localhost:8080"), "/"), Token: t}}
	}
	ts, err := loadHookTokens()
	if err != nil {
		return nil
	}
	return ts
}

// rewake runs as a Claude Code hook, so it never fails loudly: anything other
// than exit 2 is ignored by Claude Code. With several saved tokens (one per
// group) it waits on all of them and wakes on the first doorbell.
func rewake(ctx context.Context, log *slog.Logger) int {
	ts := hookTokens()
	if len(ts) == 0 {
		return 0
	}
	dir := stateDir()
	if dir == "" {
		return 0
	}
	return rewakeAll(ctx, ts, dir, log, os.Stderr)
}

// stateDir is where the hooks keep their state: HUDDLE_STATE_DIR, or
// huddle-mcp under the user's cache directory ("" if there is none).
func stateDir() string {
	if d := os.Getenv("HUDDLE_STATE_DIR"); d != "" {
		return d
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cache, "huddle-mcp")
}

func rewakeAll(ctx context.Context, ts []hookToken, dir string, log *slog.Logger, errOut io.Writer) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	outs := make([]strings.Builder, len(ts))
	codes := make([]int, len(ts))
	var wg sync.WaitGroup
	for i, t := range ts {
		wg.Go(func() {
			codes[i] = channel.Rewake(ctx, channel.Config{
				BaseURL: t.URL, Token: t.Token, Version: version, Log: log,
			}, dir, &outs[i])
			if codes[i] == 2 {
				cancel() // wake now; the others stop and keep their place
			}
		})
	}
	wg.Wait()
	code := 0
	for i := range ts {
		if codes[i] == 2 {
			// Each saved its position before returning, so print them all.
			_, _ = io.WriteString(errOut, outs[i].String())
			code = 2
		}
	}
	return code
}

// inboxHook is the UserPromptSubmit hook: new events for every saved token,
// printed into the prompt. Silent when nothing is new or a server is down.
func inboxHook(ctx context.Context, out io.Writer) {
	client := &http.Client{Timeout: 2 * time.Second}
	for _, t := range hookTokens() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL+"/hook/inbox", nil)
		if err != nil {
			continue
		}
		req.Header.Set("Authorization", "Bearer "+t.Token)
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode == http.StatusOK {
			_, _ = io.Copy(out, io.LimitReader(resp.Body, 64<<10))
		}
		resp.Body.Close()
	}
}

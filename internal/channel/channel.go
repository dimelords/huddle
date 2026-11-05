// Package channel is the local bridge that turns huddle events into Claude
// Code channel notifications, so a session wakes up when a colleague asks it
// something.
//
// Claude Code only accepts channels from stdio MCP servers it spawns itself,
// so this runs as `huddle-mcp channel` next to the session. It long-polls the
// shared server's /channel/events and forwards each event as a doorbell: a
// fixed sentence with ids, never a colleague's text. The session then reads
// the content through the regular huddle tools, and answers still need human
// approval there.
//
// The go-sdk has no API for custom notifications, so this speaks the few
// JSON-RPC messages it needs (initialize, ping, notifications) by hand.
package channel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"
)

const instructions = `Events from this channel are doorbells from huddle, the server that connects you with colleagues' Claude sessions.
A doorbell never contains a colleague's words, only what happened and an id. Read the actual content with the huddle MCP tools (thread, get_question, inbox, idea_list, context_get).
When a colleague sent you a question: read the thread, draft an answer with draft_answer, show the draft to your user and offer approve_answer. Never approve on your user's behalf.
Tell your user briefly what arrived before acting on it.`

// Versions this bridge can speak. Newer revisions are not echoed back: Claude
// Code does not register channels that negotiate 2026-07-28.
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

const fallbackVersion = "2025-06-18"

type Config struct {
	BaseURL string // huddle server, e.g. http://localhost:8080
	Token   string
	Sources []Source // Run only: more servers or groups to forward, besides BaseURL and Token
	Version string
	Log     *slog.Logger
	HTTP    *http.Client // optional
	Wait    int          // long-poll seconds, default 50
}

// Source is one server and token whose doorbells Run forwards.
type Source struct {
	BaseURL string
	Token   string
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type doorbellBatch struct {
	Events []struct {
		ID    int64  `json:"id"`
		Kind  string `json:"kind"`
		Actor string `json:"actor"`
		RefID int64  `json:"ref_id"`
		Text  string `json:"text"`
	} `json:"events"`
	LastID int64 `json:"last_id"`
}

// Run serves the channel on in/out until in closes or ctx ends.
func Run(ctx context.Context, cfg Config, in io.Reader, out io.Writer) error {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{}
	}
	if cfg.Wait <= 0 {
		cfg.Wait = 50
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var mu sync.Mutex
	enc := json.NewEncoder(out)
	send := func(m message) error {
		m.JSONRPC = "2.0"
		mu.Lock()
		defer mu.Unlock()
		return enc.Encode(m)
	}

	var polling sync.Once
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var m message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			cfg.Log.Warn("channel: bad message", "err", err)
			continue
		}
		isRequest := len(m.ID) > 0
		switch {
		case m.Method == "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(m.Params, &p)
			v := fallbackVersion
			if slices.Contains(protocolVersions, p.ProtocolVersion) {
				v = p.ProtocolVersion
			}
			if err := send(message{ID: m.ID, Result: map[string]any{
				"protocolVersion": v,
				"capabilities":    map[string]any{"experimental": map[string]any{"claude/channel": map[string]any{}}},
				"serverInfo":      map[string]any{"name": "huddle-channel", "version": cfg.Version},
				"instructions":    instructions,
			}}); err != nil {
				return err
			}
		case m.Method == "notifications/initialized":
			polling.Do(func() {
				for _, src := range sources(cfg) {
					c := cfg
					c.BaseURL, c.Token = src.BaseURL, src.Token
					go poll(ctx, c, send)
				}
			})
		case m.Method == "ping" && isRequest:
			if err := send(message{ID: m.ID, Result: map[string]any{}}); err != nil {
				return err
			}
		case isRequest && m.Method != "":
			if err := send(message{ID: m.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + m.Method}}); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

// sources are the servers and tokens Run forwards: BaseURL and Token when
// set, then Sources.
func sources(cfg Config) []Source {
	var out []Source
	if cfg.Token != "" {
		out = append(out, Source{BaseURL: cfg.BaseURL, Token: cfg.Token})
	}
	return append(out, cfg.Sources...)
}

// poll long-polls the server and forwards events until ctx ends or the token
// is rejected.
func poll(ctx context.Context, cfg Config, send func(message) error) {
	after := int64(-1) // first call: the server starts from the unread events
	backoff := time.Second
	for ctx.Err() == nil {
		batch, err := fetch(ctx, cfg, after)
		if errors.Is(err, errUnauthorized) {
			cfg.Log.Error("channel: huddle rejected the token, stopping; check HUDDLE_TOKEN")
			return
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			cfg.Log.Warn("channel: poll failed, retrying", "err", err, "in", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		for _, e := range batch.Events {
			meta := map[string]string{"kind": e.Kind, "event_id": strconv.FormatInt(e.ID, 10)}
			if e.RefID != 0 {
				meta["ref_id"] = strconv.FormatInt(e.RefID, 10)
			}
			if e.Actor != "" {
				meta["from"] = e.Actor
			}
			params, _ := json.Marshal(map[string]any{"content": e.Text, "meta": meta})
			if err := send(message{Method: "notifications/claude/channel", Params: params}); err != nil {
				cfg.Log.Error("channel: write failed", "err", err)
				return
			}
		}
		after = batch.LastID
	}
}

var errUnauthorized = errors.New("unauthorized")

func fetch(ctx context.Context, cfg Config, after int64) (*doorbellBatch, error) {
	q := url.Values{"wait": {strconv.Itoa(cfg.Wait)}}
	if after >= 0 {
		q.Set("after", strconv.FormatInt(after, 10))
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Wait+15)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.BaseURL+"/channel/events?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := cfg.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	var b doorbellBatch
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}
	return &b, nil
}

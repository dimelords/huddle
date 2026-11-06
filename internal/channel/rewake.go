package channel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Rewake is the fallback for when channels are blocked: a Claude Code hook
// with asyncRewake runs it in the background (on SessionStart and after
// every turn via Stop). It long-polls the same doorbells as the channel and,
// when one arrives, prints it to errOut and returns exit code 2, which wakes
// Claude. It returns 0 when there is nothing to do: another poller already
// runs for this member, the token is rejected, or the session went away.
func Rewake(ctx context.Context, cfg Config, stateDir string, errOut io.Writer) int {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{}
	}
	if cfg.Wait <= 0 {
		cfg.Wait = 25
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		cfg.Log.Error("rewake: state dir", "err", err)
		return 0
	}
	sum := sha256.Sum256([]byte(cfg.Token))
	base := filepath.Join(stateDir, "rewake-"+hex.EncodeToString(sum[:8]))

	// One poller per member: the Stop hook fires after every turn.
	lock, err := os.OpenFile(base+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		cfg.Log.Error("rewake: lock", "err", err)
		return 0
	}
	defer lock.Close()
	if err := tryLock(lock); err != nil {
		return 0
	}

	after := int64(-1)
	if b, err := os.ReadFile(base + ".last"); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
			after = n
		}
	}
	save := func(id int64) {
		if err := os.WriteFile(base+".last", []byte(strconv.FormatInt(id, 10)), 0o600); err != nil {
			cfg.Log.Warn("rewake: save position", "err", err)
		}
	}

	// Stop when the session that started the hook is gone: nobody would be woken.
	alive, release := watchSession()
	defer release()
	backoff := time.Second
	for ctx.Err() == nil && alive() {
		batch, err := fetch(ctx, cfg, after)
		if errors.Is(err, errUnauthorized) {
			cfg.Log.Error("rewake: huddle rejected the token; check HUDDLE_TOKEN")
			return 0
		}
		if err != nil {
			select {
			case <-ctx.Done():
				return 0
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		save(batch.LastID)
		after = batch.LastID
		if len(batch.Events) == 0 {
			continue
		}
		fmt.Fprintf(errOut, "[huddle] %d new event(s):\n", len(batch.Events))
		for _, e := range batch.Events {
			fmt.Fprintf(errOut, "- %s\n", e.Text)
		}
		fmt.Fprintln(errOut, "These are doorbells from huddle-mcp: read the content with the huddle tools, tell your user what arrived, and never approve an answer on their behalf.")
		return 2
	}
	return 0
}

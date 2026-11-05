package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTranscript writes a transcript with n tool calls; saveAt > 0 makes
// call number saveAt a memory_save.
func writeTranscript(t *testing.T, path string, n, saveAt int) {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"type":"user","message":{"role":"user","content":"hej"}}` + "\n")
	for i := 1; i <= n; i++ {
		name := "Bash"
		if i == saveAt {
			name = "mcp__huddle__memory_save"
		}
		fmt.Fprintf(&b, `{"type":"assistant","message":{"content":[{"type":"text","text":"x"},{"type":"tool_use","name":%q,"input":{}}]}}`+"\n", name)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryHook(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HUDDLE_TOKEN", "hdl_test")
	t.Setenv("HUDDLE_MEMORY_NUDGE", "")
	tr := filepath.Join(dir, "t.jsonl")
	state := filepath.Join(dir, "state")
	run := func(active bool) string {
		var out strings.Builder
		in := fmt.Sprintf(`{"session_id":"s/1","transcript_path":%q,"stop_hook_active":%v}`, tr, active)
		memoryHook(strings.NewReader(in), &out, state)
		return out.String()
	}

	writeTranscript(t, tr, memoryNudgeAfter-1, 0)
	if got := run(false); got != "" {
		t.Fatalf("too early: %s", got)
	}
	writeTranscript(t, tr, memoryNudgeAfter, 0)
	if got := run(true); got != "" {
		t.Fatalf("must not block while a Stop hook is active: %s", got)
	}
	if got := run(false); !strings.Contains(got, `"decision":"block"`) || !strings.Contains(got, "memory_save") {
		t.Fatalf("want a nudge, got %q", got)
	}
	if got := run(false); got != "" {
		t.Fatalf("nudged twice for the same work: %s", got)
	}

	// A memory_save counts as considering the memory.
	writeTranscript(t, tr, 2*memoryNudgeAfter, memoryNudgeAfter+5)
	if got := run(false); got != "" {
		t.Fatalf("saved recently, should not nudge: %s", got)
	}
	writeTranscript(t, tr, 2*memoryNudgeAfter+5, memoryNudgeAfter+5)
	if got := run(false); !strings.Contains(got, "block") {
		t.Fatalf("want a nudge after more work, got %q", got)
	}

	t.Setenv("HUDDLE_MEMORY_NUDGE", "off")
	writeTranscript(t, tr, 10*memoryNudgeAfter, 0)
	if got := run(false); got != "" {
		t.Fatalf("turned off: %s", got)
	}
}

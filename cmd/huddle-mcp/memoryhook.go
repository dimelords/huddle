package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// memoryNudgeAfter is how many tool calls a session makes, without saving to
// the group memory, before the Stop hook reminds it to consider doing so.
const memoryNudgeAfter = 15

const memoryNudge = "huddle: this session has done a fair amount of work since it last considered the group's shared memory. " +
	"If the work produced something a colleague would want later (a decision and why, a solved non-obvious problem with symptom, cause and fix, " +
	"a convention, a how-to that worked), search the memory with memory_search and save or update an entry with memory_save, " +
	"then tell your user in one line what you saved. If nothing qualifies, stop without mentioning this reminder."

// stopInput is the part of Claude Code's Stop hook input the memory hook uses.
type stopInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	StopHookActive bool   `json:"stop_hook_active"`
}

// memoryHook is the Stop hook that reminds the session to save to the group
// memory. It blocks the stop (one more turn) at most once per
// memoryNudgeAfter tool calls, and never while a Stop hook already made the
// session continue. Like the other hooks it never fails loudly.
func memoryHook(in io.Reader, out io.Writer, stateDir string) {
	if stateDir == "" || os.Getenv("HUDDLE_MEMORY_NUDGE") == "off" || len(hookTokens()) == 0 {
		return
	}
	var si stopInput
	if err := json.NewDecoder(in).Decode(&si); err != nil || si.StopHookActive || si.SessionID == "" || si.TranscriptPath == "" {
		return
	}
	calls, lastSave := countToolCalls(si.TranscriptPath)
	state := filepath.Join(stateDir, "memory-nudge-"+safeName(si.SessionID))
	since := 0
	if b, err := os.ReadFile(state); err == nil {
		since, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	since = max(since, lastSave)
	if calls-since < memoryNudgeAfter {
		return
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return
	}
	if err := os.WriteFile(state, []byte(strconv.Itoa(calls)), 0o600); err != nil {
		return // without state it would nudge at every stop
	}
	_ = json.NewEncoder(out).Encode(map[string]string{"decision": "block", "reason": memoryNudge})
}

// countToolCalls reads a Claude Code transcript (JSON lines) and returns the
// number of tool calls, and how many there had been at the last memory_save.
func countToolCalls(path string) (calls, lastSave int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var line struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		var parts []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if json.Unmarshal(line.Message.Content, &parts) != nil {
			continue // a plain string message
		}
		for _, p := range parts {
			if p.Type != "tool_use" {
				continue
			}
			calls++
			if strings.HasSuffix(p.Name, "memory_save") {
				lastSave = calls
			}
		}
	}
	return calls, lastSave
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func safeName(s string) string { return unsafeChars.ReplaceAllString(s, "_") }

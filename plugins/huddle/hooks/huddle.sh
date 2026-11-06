#!/bin/sh
# Runs a huddle-mcp hook (rewake, inbox-hook or memory-hook) when the command is
# installed, and does nothing when it is not. Claude Code starts hooks
# without the login shell's PATH, so the usual install places are tried too.
# On Windows Claude Code runs hooks in Git Bash. Scoop's own copy is tried
# before PATH, which finds its shim: that would only start the copy as one
# more process.
scoop="${SCOOP:-$HOME/scoop}"
for bin in "$scoop/apps/huddle-mcp/current/huddle-mcp.exe" "$(command -v huddle-mcp 2>/dev/null)" \
	/opt/homebrew/bin/huddle-mcp /usr/local/bin/huddle-mcp /home/linuxbrew/.linuxbrew/bin/huddle-mcp \
	"$HOME/go/bin/huddle-mcp" "$HOME/go/bin/huddle-mcp.exe"; do
	[ -n "$bin" ] && [ -x "$bin" ] && exec "$bin" "$@"
done
exit 0

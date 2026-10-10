#!/usr/bin/env bash
# PostToolUse hook: rebuild dist/vito after Claude edits a source file, and
# restart the running daemon. The UI lives inside the binary (web/web.go embeds
# index.html), so without a rebuild + restart an edit is invisible in the PWA.
#
# Claude Code runs hooks through Git Bash on Windows, so this one entry serves
# every OS: Windows goes on to rebuild.ps1, Linux and macOS are handled here.
set -u

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
case "$(uname -s)" in
  MINGW* | MSYS* | CYGWIN*) exec pwsh -NoProfile -File "$here/rebuild.ps1" ;;
esac

repo="${CLAUDE_PROJECT_DIR:-$(dirname "$(dirname "$here")")}"

raw="$(cat)"
if command -v jq >/dev/null 2>&1; then
  f="$(printf '%s' "$raw" | jq -r '.tool_input.file_path // empty' 2>/dev/null)"
else
  f="$(printf '%s' "$raw" | sed -n 's/.*"file_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
fi
[ -n "$f" ] || exit 0
[[ "$f" =~ \.(go|html|js|css|webmanifest|mod|sum)$ ]] || exit 0
[[ "$f" == "$repo"/* ]] || exit 0

exe="$repo/dist/vito"
if ! out="$(cd "$repo" && go build -o "$exe" ./cmd/vito 2>&1)"; then
  # Exit 2 feeds the compiler output back to Claude so it can fix the break.
  printf 'go build failed:\n%s\n' "$out" >&2
  exit 2
fi

# Only restart if a daemon was already running from dist/ — never start one
# unasked, and leave an installed Vito (AppImage, Flatpak, package) alone.
# Match on the executable path: after go build replaces the file, a running
# process's exe link reads "<path> (deleted)".
pids=()
if [ -d /proc ]; then
  for p in /proc/[0-9]*; do
    t="$(readlink "$p/exe" 2>/dev/null)" || continue
    if [ "$t" = "$exe" ] || [ "$t" = "$exe (deleted)" ]; then pids+=("${p#/proc/}"); fi
  done
else
  while read -r pid; do pids+=("$pid"); done < <(pgrep -f "^$exe serve" 2>/dev/null)
fi

if [ "${#pids[@]}" -gt 0 ]; then
  kill "${pids[@]}" 2>/dev/null
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    kill -0 "${pids[@]}" 2>/dev/null || break
    sleep 0.2
  done
  kill -9 "${pids[@]}" 2>/dev/null
  detach=nohup; command -v setsid >/dev/null 2>&1 && detach=setsid
  (cd "$repo" && "$detach" "$exe" serve >/dev/null 2>&1 < /dev/null &)
  msg='Vito herbouwd + daemon herstart — ververs de PWA (Ctrl+R)'
else
  msg='Vito herbouwd (daemon draaide niet)'
fi

printf '{"systemMessage":"%s","suppressOutput":true}\n' "$msg"

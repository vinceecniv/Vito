#!/usr/bin/env bash
# Fast PR lane for a protected main: branch off, commit everything, open a PR and
# auto-merge (squash) as soon as build-test is green. For the "see it, fix it,
# ship it" flow without ever pushing straight to main.
#
# Requires: gh (GitHub CLI, authenticated) and "Allow auto-merge" enabled on the repo.
# Usage:  scripts/ship.sh "commit message" [branch-name]
set -euo pipefail
msg="${1:?usage: scripts/ship.sh \"commit message\" [branch-name]}"

start=$(git rev-parse --abbrev-ref HEAD)
[ "$start" = "main" ] || echo "note: branching off '$start' (normally run this from main)"

slug=$(printf '%s' "$msg" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9' '-' | sed 's/-\{2,\}/-/g; s/^-//; s/-$//' | cut -c1-40)
branch="${2:-${slug:-change}}"

git switch -c "$branch"
git add -A
git commit -m "$msg"
git push -u origin "$branch"
gh pr create --fill
gh pr merge --squash --auto --delete-branch
git switch "$start"

echo "Shipped '$msg' on branch '$branch'."
echo "The PR auto-merges once build-test is green; run 'git pull' on $start afterwards."

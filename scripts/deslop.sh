#!/usr/bin/env bash
# deslop: reject tool-attribution strings and machine-voice tells before
# they reach the repo. Patterns live in deslop-patterns.txt; escape a
# legitimate hit with an inline "deslop:allow <reason>".
#
#   deslop.sh                  scan the staged diff (pre-commit)
#   deslop.sh --message FILE   scan a commit message (commit-msg hook)
#   deslop.sh --range A..B     scan diffs and messages of a range (CI)
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
patterns="$root/scripts/deslop-patterns.txt"
exclude=":(exclude)scripts/deslop-patterns.txt"

active_patterns() {
    grep -vE '^(#|$)' "$patterns"
}

# scan LABEL — reads stdin, prints offending lines, returns 1 on any hit.
scan() {
    local label="$1" hits
    hits="$(grep -nE -f <(active_patterns) - | grep -v 'deslop:allow' || true)"
    if [ -n "$hits" ]; then
        printf 'deslop: %s:\n%s\n' "$label" "$hits" >&2
        return 1
    fi
}

rc=0
case "${1:-}" in
--message)
    scan "commit message" <"$2" || rc=1
    ;;
--range)
    git log --format=%B "$2" | scan "commit messages in $2" || rc=1
    git diff "$2" -- . "$exclude" | grep '^+' | scan "diff $2" || rc=1
    ;;
*)
    git diff --cached -- . "$exclude" | grep '^+' | scan "staged changes" || rc=1
    ;;
esac
exit $rc

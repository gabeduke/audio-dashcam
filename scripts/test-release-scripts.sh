#!/usr/bin/env bash
# Exercise the release helper scripts without pushing anything.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fail=0

check() {
  local name="$1" want="$2" got="$3"
  if [ "$want" = "$got" ]; then
    echo "ok   - $name"
  else
    echo "FAIL - $name"
    echo "       want: $want"
    echo "       got:  $got"
    fail=1
  fi
}

# next-tag.sh reads existing tags on stdin and takes the date as $1.
check "first release of the day" \
  "v2026.09.09.1" \
  "$(printf '' | "$HERE/next-tag.sh" 2026.09.09)"

check "second release of the day increments" \
  "v2026.09.09.2" \
  "$(printf 'v2026.09.09.1\n' | "$HERE/next-tag.sh" 2026.09.09)"

check "counts only today's tags" \
  "v2026.09.09.1" \
  "$(printf 'v2026.09.08.1\nv2026.09.08.2\n' | "$HERE/next-tag.sh" 2026.09.09)"

check "does not trip over a double-digit count" \
  "v2026.09.09.11" \
  "$(printf 'v2026.09.09.%s\n' 1 2 3 4 5 6 7 8 9 10 | "$HERE/next-tag.sh" 2026.09.09)"

# changelog-entry.sh renders a section from lines of "subject (sha)" on stdin.
entry="$(printf -- '- Do a thing (abc1234)\n- Do another (def5678)\n' \
  | "$HERE/changelog-entry.sh" v2026.09.09.1 2026-09-09)"
case "$entry" in
  "## v2026.09.09.1 — 2026-09-09"*) echo "ok   - entry starts with the heading" ;;
  *) echo "FAIL - entry heading: got ${entry%%$'\n'*}"; fail=1 ;;
esac
case "$entry" in
  *"- Do another (def5678)"*) echo "ok   - entry carries every commit" ;;
  *) echo "FAIL - entry dropped a commit"; fail=1 ;;
esac

exit "$fail"

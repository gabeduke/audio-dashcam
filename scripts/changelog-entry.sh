#!/usr/bin/env bash
# Render one CHANGELOG section from pre-formatted commit lines on stdin.
#
#   git log --no-merges --pretty='- %s (%h)' "$range" \
#     | scripts/changelog-entry.sh v2026.09.09.1 2026-09-09
set -euo pipefail

tag="${1:?usage: changelog-entry.sh TAG DATE  (commit lines on stdin)}"
date="${2:?usage: changelog-entry.sh TAG DATE  (commit lines on stdin)}"

body="$(cat)"
[ -n "$body" ] || body="- No changes recorded."

printf '## %s — %s\n\n%s\n\n' "$tag" "$date" "$body"

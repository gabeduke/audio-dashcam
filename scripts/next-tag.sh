#!/usr/bin/env bash
# Print the next CalVer release tag for a date, given the existing tags on
# stdin. Releases are cut per merge, so several can land on one day.
#
#   git tag -l | scripts/next-tag.sh 2026.09.09   ->   v2026.09.09.3
set -euo pipefail

date="${1:?usage: next-tag.sh YYYY.MM.DD  (existing tags on stdin)}"

# The next tag is max(existing suffixes) + 1, not count + 1: a deleted tag
# (the routine response to a bad release) leaves a gap, and count+1 would
# recompute a name that already exists -- colliding for the rest of the UTC
# day. `|| true` covers both an empty match set and grep's exit status 1 on
# no match, so `n` is unset either way and `${n:-0}` supplies the 0.
n=$(grep "^v${date}\.[0-9][0-9]*$" | sed 's/.*\.//' | sort -n | tail -1 || true)
echo "v${date}.$(( ${n:-0} + 1 ))"

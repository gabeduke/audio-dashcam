#!/usr/bin/env bash
# Print the next CalVer release tag for a date, given the existing tags on
# stdin. Releases are cut per merge, so several can land on one day.
#
#   git tag -l | scripts/next-tag.sh 2026.09.09   ->   v2026.09.09.3
set -euo pipefail

date="${1:?usage: next-tag.sh YYYY.MM.DD  (existing tags on stdin)}"

# grep -c would count matches; grep then wc keeps an empty input at 0 without
# tripping the pipeline's errexit on grep's exit status 1.
n=$(grep -c "^v${date}\.[0-9][0-9]*$" || true)
echo "v${date}.$((n + 1))"

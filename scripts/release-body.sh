#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 || -z $1 ]]; then
  printf 'Usage: %s <tag>\n' "$0" >&2
  exit 2
fi

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
tag=$1
notes=$(awk -v tag="$tag" '
  $0 == "# " tag { in_section = 1; next }
  in_section && /^# v/ { exit }
  in_section && !content && $0 !~ /[^[:space:]]/ { next }
  in_section { lines = lines $0 "\n"; if ($0 ~ /[^[:space:]]/) content = 1 }
  END { if (content) printf "%s", lines }
' "$root/releases/RELEASE_NOTES.md")

cat "$root/releases/QUICK_INSTALL.md"
if [[ -n $notes ]]; then
  printf '\n%s\n' "$notes"
else
  printf '::warning::no release notes section for %s\n' "$tag" >&2
fi

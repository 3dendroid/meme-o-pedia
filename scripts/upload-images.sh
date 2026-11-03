#!/usr/bin/env bash
# Downloads meme pictures from the internet and uploads them through the API.
# Run from the repository root: ./scripts/upload-images.sh [list-file]
#
# The list file (default: images.txt) has one meme per line:
#   Meme title | https://example.com/picture.png
# The title must match the meme title on the site (case does not matter).
# Empty lines and lines starting with # are skipped.
set -euo pipefail

LIST=${1:-images.txt}
BASE_URL=${BASE_URL:-http://localhost:8080}

if [[ ! -f .env ]]; then
  echo "No .env file: run this from the repository root." >&2
  exit 1
fi
# shellcheck disable=SC1091
source .env
: "${ADMIN_TOKEN:?ADMIN_TOKEN is not set in .env}"
if [[ ! -f "$LIST" ]]; then
  echo "No list file $LIST. Copy images.example.txt to $LIST and add the links." >&2
  exit 1
fi

if ! curl -sf "$BASE_URL/readyz" >/dev/null; then
  echo "The API at $BASE_URL is not answering. Is the cluster running? Set BASE_URL if it is elsewhere." >&2
  exit 1
fi

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
ok=0
failed=0

while IFS='|' read -r title url; do
  # Trim spaces around both parts (titles may contain quotes and apostrophes).
  title=$(sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' <<<"$title")
  url=$(sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' <<<"$url")
  [[ -z "$title" || "$title" == \#* ]] && continue
  if [[ -z "$url" ]]; then
    echo "SKIP  $title: no link"
    failed=$((failed + 1))
    continue
  fi

  # Find the meme ID by its exact title.
  id=$(curl -sf --get "$BASE_URL/api/memes" --data-urlencode "q=$title" |
    python3 -c 'import json, sys
title = sys.argv[1].lower()
print(next((m["id"] for m in json.load(sys.stdin) if m["title"].lower() == title), ""))' "$title")
  if [[ -z "$id" ]]; then
    echo "SKIP  $title: no meme with this title"
    failed=$((failed + 1))
    continue
  fi

  if ! curl -sfL --max-time 60 -A "meme-o-pedia-uploader" -o "$tmp" "$url"; then
    echo "FAIL  $title: could not download $url"
    failed=$((failed + 1))
    continue
  fi

  # The API answers with the meme as JSON, or with {"error": "..."}.
  answer=$(curl -s -X PUT "$BASE_URL/api/memes/$id/image" \
    -H "Authorization: Bearer $ADMIN_TOKEN" --data-binary @"$tmp")
  if [[ "$answer" == *'"image_url"'* ]]; then
    echo "OK    $title (id $id)"
    ok=$((ok + 1))
  else
    echo "FAIL  $title: $answer"
    failed=$((failed + 1))
  fi
done < "$LIST"

echo
echo "Uploaded: $ok, failed or skipped: $failed"

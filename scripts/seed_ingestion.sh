#!/usr/bin/env bash
set -euo pipefail

BASE_URL=${BASE_URL:-http://localhost:8080}
AUTH=${AUTH:-"-u kiali:developer"}

# Mode: docs | videos | all (default all)
MODE=${1:-all}

# Space or newline separated list of doc URLs to ingest
DOC_URLS_STR=${DOC_URLS_STR:-"https://kiali.io/documentation/"}
# Space or newline separated list of YouTube transcript/page or playlist URLs to ingest (optional)
YT_URLS_STR=${YT_URLS_STR:-"https://www.youtube.com/playlist?list=PLgwrCYrAkQYOHIeGukEZUmNRGbqI9uwpi https://www.youtube.com/playlist?list=PLgwrCYrAkQYOB9IHOkb1g624izSEGYPWk https://www.youtube.com/playlist?list=PLgwrCYrAkQYOip2YTIwy8jfNB9giddbBL https://www.youtube.com/playlist?list=PLgwrCYrAkQYNSMhQv1JEbCC6M88GpE0kF"}

# Split into arrays
read -r -a DOC_URLS <<< "$DOC_URLS_STR"
read -r -a YT_URLS <<< "$YT_URLS_STR"

if [[ "$MODE" == "all" || "$MODE" == "docs" ]]; then
  # Ingest Kiali docs (one request per URL)
  for u in "${DOC_URLS[@]}"; do
    if [[ -n "$u" ]]; then
      echo "Ingesting docs: $u"
      curl -sS -X POST "$BASE_URL/v1/ingest/kiali-docs" \
        $AUTH -H 'Content-Type: application/json' \
        -d "{\"base_url\":\"$u\"}" | jq . || true
    fi
  done
fi

if [[ "$MODE" == "all" || "$MODE" == "videos" ]]; then
  # Ingest YouTube transcripts/pages or expand playlists (one request per URL)
  for u in "${YT_URLS[@]}"; do
    if [[ -n "$u" ]]; then
      echo "Ingesting YouTube: $u"
      curl -sS -X POST "$BASE_URL/v1/ingest/youtube" \
        $AUTH -H 'Content-Type: application/json' \
        -d "{\"channel_or_playlist_url\":\"$u\"}" | jq . || true
    fi
  done
fi

echo "Seeding complete." 
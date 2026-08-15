#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 1 || -z "$1" ]]; then
  echo "usage: $0 PROJECT_ID" >&2
  exit 2
fi

project_id=$1

for collection_group in oauth_login_attempts sessions api_token_rate_limits; do
  gcloud firestore fields ttls update expires_at \
    --collection-group="$collection_group" \
    --database="(default)" \
    --project="$project_id" \
    --enable-ttl \
    --quiet
done

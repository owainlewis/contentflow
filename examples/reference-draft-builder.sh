#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: reference-draft-builder.sh <transcript-file>" >&2
  exit 2
fi

jq -Rs '. as $transcript |
($transcript | gsub("\\s+"; " ") | .[:240]) as $excerpt | {
  items: [
    range(1; 21) as $number |
    {
      type: "x",
      working_title: ("Standalone draft " + ($number | tostring)),
      status: "draft",
      content: {body: ($excerpt + " [" + ($number | tostring) + "]")}
    }
  ]
}' "$1"

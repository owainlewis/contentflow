#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: reference-agent-sandbox.sh <draft-builder> <transcript-file>" >&2
  exit 2
fi

if [[ ! -f "$1" || ! -x "$1" ]]; then
  echo "draft builder must be an executable file" >&2
  exit 2
fi
if [[ ! -f "$2" || ! -r "$2" ]]; then
  echo "transcript must be a readable file" >&2
  exit 2
fi

builder=$(cd -- "$(dirname -- "$1")" && pwd -P)/$(basename -- "$1")
transcript=$(cd -- "$(dirname -- "$2")" && pwd -P)/$(basename -- "$2")
safe_path=/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin

case $(uname -s) in
  Darwin)
    if [[ ! -x /usr/bin/sandbox-exec ]]; then
      echo "reference agent requires sandbox-exec" >&2
      exit 2
    fi
    profile=$(mktemp)
    cleanup() { rm -f "$profile"; }
    trap cleanup EXIT
    escaped_builder=${builder//\\/\\\\}
    escaped_builder=${escaped_builder//\"/\\\"}
    escaped_transcript=${transcript//\\/\\\\}
    escaped_transcript=${escaped_transcript//\"/\\\"}
    printf '%s\n' \
      '(version 1)' \
      '(deny default)' \
      '(allow process-fork)' \
      '(allow process-exec*)' \
      '(allow process-info* (target self))' \
      '(allow sysctl-read)' \
      '(allow file-read* (literal "/") (literal "/bin") (literal "/usr") (literal "/System") (literal "/Library") (literal "/opt") (literal "/opt/homebrew") (literal "/etc") (literal "/var") (literal "/tmp") (literal "/private") (literal "/private/var") (literal "/private/var/select") (literal "/private/var/select/sh"))' \
      '(allow file-read* (subpath "/bin") (subpath "/usr/bin") (subpath "/usr/lib") (subpath "/usr/share") (subpath "/System/Library") (subpath "/Library/Apple") (subpath "/opt/homebrew/bin") (subpath "/opt/homebrew/Cellar") (subpath "/opt/homebrew/lib") (subpath "/opt/homebrew/opt") (subpath "/opt/homebrew/share"))' \
      '(allow file-read* (literal "/dev/null") (literal "/dev/urandom") (literal "'"$escaped_builder"'") (literal "'"$escaped_transcript"'"))' \
      '(allow file-write* (literal "/dev/null"))' \
      >"$profile"
    cd /
    set +e
    /usr/bin/sandbox-exec -f "$profile" /usr/bin/env -i PATH="$safe_path" \
      "$builder" "$transcript" </dev/null
    status=$?
    set -e
    cleanup
    trap - EXIT
    exit "$status"
    ;;
  Linux)
    if ! command -v bwrap >/dev/null 2>&1; then
      echo "reference agent requires bubblewrap (bwrap)" >&2
      exit 2
    fi
    mounts=(--ro-bind /usr /usr --proc /proc --dev /dev --tmpfs /tmp --dir /work)
    for directory in /bin /sbin /lib /lib64; do
      if [[ -e "$directory" ]]; then
        mounts+=(--ro-bind "$directory" "$directory")
      fi
    done
    exec env -i PATH="$safe_path" bwrap --die-with-parent --unshare-all --new-session \
      "${mounts[@]}" --ro-bind "$builder" /work/draft-builder \
      --ro-bind "$transcript" /work/transcript --chdir /work \
      /work/draft-builder /work/transcript </dev/null
    ;;
  *)
    echo "reference agent sandbox is unsupported on this operating system" >&2
    exit 2
    ;;
esac

#!/usr/bin/env bash
set +x
set -euo pipefail

if [[ ${1:-} == "--contentflow-clean-environment" ]]; then
  IFS= read -r contentflow_api_url <&3
  IFS= read -r contentflow_api_token <&4
  source /dev/fd/5
  exec 3<&- 4<&- 5<&-
  shift
else
  if [[ ${1:-} == "--replay" ]]; then
    if [[ $# -ne 2 ]]; then
      echo "usage: reference-agent.sh --replay <recovery-file>" >&2
      exit 2
    fi
  elif [[ $# -ne 3 ]]; then
    echo "usage: reference-agent.sh <youtube-id> <draft-builder> <operation-id>" >&2
    exit 2
  fi
  contentflow_api_url=${CONTENTFLOW_API_URL:?CONTENTFLOW_API_URL is required}
  contentflow_api_token=${CONTENTFLOW_API_TOKEN:?CONTENTFLOW_API_TOKEN is required}
  export -n contentflow_api_url contentflow_api_token
  exec 3<<<"$contentflow_api_url"
  exec 4<<<"$contentflow_api_token"
  unset CONTENTFLOW_API_URL CONTENTFLOW_API_TOKEN
  environment_exports=$(export -p)
  exec 5<<<"$environment_exports"
  unset environment_exports contentflow_api_url contentflow_api_token
  exec -c /bin/bash "$0" --contentflow-clean-environment "$@"
fi
if [[ -z "$contentflow_api_url" || -z "$contentflow_api_token" ]]; then
  echo "ContentFlow credentials are required" >&2
  exit 2
fi
repository_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
flow_bin=${FLOW_BIN:-"$repository_root/bin/flow"}
max_batch_bytes=1048448
sha256_file() {
  local digest output
  if command -v sha256sum >/dev/null 2>&1; then
    output=$(sha256sum -- "$1" 2>/dev/null) || return 2
  elif command -v shasum >/dev/null 2>&1; then
    output=$(shasum -a 256 -- "$1" 2>/dev/null) || return 2
  else
    return 2
  fi
  digest=${output%% *}
  [[ "$digest" =~ ^[0-9a-f]{64}$ ]] || return 2
  printf '%s' "$digest"
}
frozen_batch_open=0
freeze_batch() {
  local snapshot
  snapshot=$(mktemp "${TMPDIR:-/tmp}/contentflow-snapshot-XXXXXX")
  if ! chmod 600 "$snapshot" || ! cp -- "$1" "$snapshot" || \
    ! exec 6<"$snapshot" || ! exec 7<"$snapshot" || ! exec 8<"$snapshot" || \
    ! exec 9<"$snapshot" || ! exec 10<"$snapshot" || ! exec 11<"$snapshot"; then
    exec 6<&- 7<&- 8<&- 9<&- 10<&- 11<&-
    rm -f "$snapshot"
    return 2
  fi
  if ! rm -f "$snapshot"; then
    exec 6<&- 7<&- 8<&- 9<&- 10<&- 11<&-
    return 2
  fi
  frozen_batch_open=1
}
close_frozen_batch() {
  if [[ $frozen_batch_open -eq 1 ]]; then
    exec 6<&- 7<&- 8<&- 9<&- 10<&- 11<&-
    frozen_batch_open=0
  fi
}
validate_frozen_batch() {
  frozen_validation_error="draft builder output is not a valid batch"
  if [[ $(wc -c <&6) -gt $max_batch_bytes ]]; then
    frozen_validation_error="draft builder output exceeds the safe batch size"
    return 2
  fi
  if ! jq -e 'type == "object" and (.items | type == "array" and length >= 1 and length <= 50)' \
    /dev/fd/7 >/dev/null 2>&1; then
    return 2
  fi
  if ! frozen_request_sha256=$(sha256_file /dev/fd/8); then
    frozen_validation_error="draft builder output could not be hashed"
    return 2
  fi
  exec 6<&- 7<&- 8<&-
}
restore_frozen_batch() {
  local retained_snapshot
  retained_snapshot=$(mktemp "${TMPDIR:-/tmp}/contentflow-retained-XXXXXX") || return 2
  if ! cp -- /dev/fd/10 "$retained_snapshot" || ! chmod 600 "$retained_snapshot" || ! mv -f -- "$retained_snapshot" "$batch_file"; then
    rm -f "$retained_snapshot"
    return 2
  fi
}
if [[ ${1:-} == "--replay" ]]; then
  if ! command -v jq >/dev/null 2>&1; then
    echo "reference agent requires jq" >&2
    exit 2
  fi
  recovery_file=$2
  if [[ ! -s "$recovery_file" ]] || ! jq -e 'type == "object" and (keys == ["api_origin", "batch_file", "operation_id", "replay_deadline", "request_sha256"]) and (.api_origin | type == "string") and (.batch_file | type == "string") and (.operation_id | type == "string") and (.replay_deadline | type == "number") and (.request_sha256 | type == "string")' "$recovery_file" >/dev/null; then
    echo "retained recovery file is invalid" >&2
    exit 2
  fi
  batch_file=$(jq -r '.batch_file' "$recovery_file")
  api_origin=$(jq -r '.api_origin' "$recovery_file")
  operation_id=$(jq -r '.operation_id' "$recovery_file")
  replay_deadline=$(jq -r '.replay_deadline' "$recovery_file")
  request_sha256=$(jq -r '.request_sha256' "$recovery_file")
  if [[ ! "$operation_id" =~ ^[0-7][0-9A-HJKMNP-TV-Z]{25}$ ]]; then
    echo "operation ID must be a ULID" >&2
    exit 2
  fi
  if [[ ! "$replay_deadline" =~ ^[0-9]+$ ]]; then
    echo "retained recovery file is invalid" >&2
    exit 2
  fi
  if [[ ! -s "$batch_file" ]]; then
    echo "retained batch file is unavailable" >&2
    exit 2
  fi
  freeze_batch "$batch_file"
  trap close_frozen_batch EXIT
  if ! validate_frozen_batch || [[ ! "$request_sha256" =~ ^[0-9a-f]{64}$ ]] || [[ "$frozen_request_sha256" != "$request_sha256" ]]; then
    echo "retained batch does not match recovery record" >&2
    exit 2
  fi
  if [[ $(date -u +%s) -ge $replay_deadline ]]; then
    echo "replay deadline has passed; reconcile batch state before any new submission" >&2
    exit 2
  fi
  if [[ "$contentflow_api_url" != "$api_origin" ]]; then
    echo "retained recovery API origin does not match CONTENTFLOW_API_URL" >&2
    exit 2
  fi
  CONTENTFLOW_API_URL="$contentflow_api_url" CONTENTFLOW_API_TOKEN="$contentflow_api_token" \
    "$flow_bin" content batch-create --file /dev/fd/9 --operation-id "$operation_id" --replay-before "$replay_deadline" --json
  exit
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "reference agent requires jq" >&2
  exit 2
fi

source_id=$1
draft_builder=$2
operation_id=$3
agent_runner=${CONTENTFLOW_AGENT_RUNNER:-"$repository_root/examples/reference-agent-sandbox.sh"}
if [[ ! "$operation_id" =~ ^[0-7][0-9A-HJKMNP-TV-Z]{25}$ ]]; then
  echo "operation ID must be a ULID" >&2
  exit 2
fi
recovery_root=${CONTENTFLOW_AGENT_RECOVERY_DIR:-}
if [[ -z "$recovery_root" || "$recovery_root" != /* || ! -d "$recovery_root" ]]; then
  echo "CONTENTFLOW_AGENT_RECOVERY_DIR must be an existing absolute durable directory" >&2
  exit 2
fi
if ! recovery_directory=$(mktemp -d "$recovery_root/contentflow-reference-XXXXXX" 2>/dev/null); then
  echo "could not create the private recovery directory" >&2
  exit 2
fi
if ! chmod 700 "$recovery_directory"; then
  rmdir "$recovery_directory" 2>/dev/null || true
  echo "could not secure the private recovery directory" >&2
  exit 2
fi
batch_file=$recovery_directory/batch.json
recovery_file=$recovery_directory/recovery.json
if ! : >"$recovery_file" || ! chmod 600 "$recovery_file"; then
  rm -f "$recovery_file"
  rmdir "$recovery_directory" 2>/dev/null || true
  echo "could not create the private recovery record" >&2
  exit 2
fi
if ! transcript_file=$(mktemp 2>/dev/null); then
  rm -f "$recovery_file"
  rmdir "$recovery_directory" 2>/dev/null || true
  echo "could not create the private transcript file" >&2
  exit 2
fi
if ! builder_output=$(mktemp 2>/dev/null); then
  rm -f "$transcript_file" "$recovery_file"
  rmdir "$recovery_directory" 2>/dev/null || true
  echo "could not create the private builder file" >&2
  exit 2
fi
batch_submission_started=0
batch_succeeded=0
batch_interrupted=0
replay_deadline=0
unavailable_exit=9
interrupted() {
  batch_interrupted=1
  exit "$unavailable_exit"
}
cleanup() {
  status=$?
  signal_exit=0
  if [[ $status -ge 129 && $status -le 192 ]]; then
    signal_exit=1
  fi
  rm -f "$transcript_file" "$builder_output"
  retain_batch=0
  if [[ $batch_submission_started -eq 1 && $batch_succeeded -eq 0 && ( $status -eq $unavailable_exit || $batch_interrupted -eq 1 || $signal_exit -eq 1 ) ]]; then
    retain_batch=1
  fi
  if [[ $retain_batch -eq 1 && $frozen_batch_open -eq 1 ]]; then
    if ! restore_frozen_batch; then
      echo "could not refresh retained batch snapshot" >&2
    fi
  fi
  if [[ $retain_batch -eq 0 ]]; then
    rm -f "$batch_file" "$recovery_file"
    rmdir "$recovery_directory" 2>/dev/null || true
  elif [[ -s "$batch_file" && -s "$recovery_file" && $(sha256_file "$batch_file") == "$request_sha256" ]]; then
    printf 'batch retained for retry: %q (operation_id: %s)\n' "$batch_file" "$operation_id" >&2
    printf 'recovery retained for retry: %q\n' "$recovery_file" >&2
    echo "replay before unix time: $replay_deadline" >&2
    printf 'replay with: %q --replay %q\n' "$repository_root/examples/reference-agent.sh" "$recovery_file" >&2
  else
    echo "exact batch recovery is unavailable; reconcile batch state before any new submission" >&2
  fi
  close_frozen_batch
  trap - EXIT HUP INT TERM
  exit "$status"
}
trap cleanup EXIT
trap interrupted HUP INT TERM

CONTENTFLOW_API_URL="$contentflow_api_url" CONTENTFLOW_API_TOKEN="$contentflow_api_token" \
  "$flow_bin" content transcript "$source_id" >"$transcript_file"
set +e
set +o pipefail
"$agent_runner" "$draft_builder" "$transcript_file" 2>/dev/null | head -c "$((max_batch_bytes + 1))" >"$builder_output"
builder_pipeline_status=("${PIPESTATUS[@]}")
set -o pipefail
set -e
if [[ ${builder_pipeline_status[1]} -ne 0 ]]; then
  echo "draft builder output could not be captured" >&2
  exit 2
fi
if [[ ${builder_pipeline_status[0]} -ne 0 ]]; then
  echo "draft builder failed" >&2
  exit "${builder_pipeline_status[0]}"
fi
freeze_batch "$builder_output"
if ! validate_frozen_batch; then
  echo "$frozen_validation_error" >&2
  exit 2
fi
cp -- /dev/fd/11 "$batch_file"
chmod 600 "$batch_file"
replay_deadline=$(($(date -u +%s) + 23 * 60 * 60))
request_sha256=$frozen_request_sha256
jq -n --arg api_origin "$contentflow_api_url" --arg batch_file "$batch_file" --arg operation_id "$operation_id" --argjson replay_deadline "$replay_deadline" --arg request_sha256 "$request_sha256" \
  '{api_origin: $api_origin, batch_file: $batch_file, operation_id: $operation_id, replay_deadline: $replay_deadline, request_sha256: $request_sha256}' >"$recovery_file"
rm -f "$transcript_file" "$builder_output"
batch_submission_started=1
CONTENTFLOW_API_URL="$contentflow_api_url" CONTENTFLOW_API_TOKEN="$contentflow_api_token" \
  "$flow_bin" content batch-create --file /dev/fd/9 --operation-id "$operation_id" --replay-before "$replay_deadline" --json
batch_succeeded=1

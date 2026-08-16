# Reference external-agent flow

This flow gives an external agent only the canonical YouTube transcript, then writes standalone drafts through one atomic batch. ContentFlow does not generate copy, call an AI model, store source lineage, or expose script sections.

Configure `CONTENTFLOW_API_URL` and `CONTENTFLOW_API_TOKEN` first. The token needs `content:read` for the transcript command and `content:write` for the batch command. Set `CONTENTFLOW_AGENT_RECOVERY_DIR` to an existing absolute directory on durable storage that survives a host restart or container replacement. The script refuses to start a new generation run without it.

The reference scripts require Bash, `jq`, and either `sha256sum` or `shasum`. The default isolation runner also requires the built-in `sandbox-exec` on macOS or Bubblewrap (`bwrap`) on Linux.

```bash
mkdir -p /durable/contentflow-agent-recovery
chmod 700 /durable/contentflow-agent-recovery
export CONTENTFLOW_AGENT_RECOVERY_DIR=/durable/contentflow-agent-recovery
examples/reference-agent.sh 01J_SOURCE... /path/to/draft-builder 01J_OPERATION...
```

The caller supplies a stable operation ID before generation. The script uses it for batch creation and retains the exact batch plus a mode-0600 recovery record after an indeterminate submission failure or an interrupt during submission. Validation, hashing, and submission use separate descriptors for one unlinked frozen snapshot, so replacing or rewriting the visible batch path cannot change the submitted bytes. Before submission, the same digest-bound bytes and recovery record are linked inside a private mode-0700 directory under `CONTENTFLOW_AGENT_RECOVERY_DIR`, so they also survive `SIGKILL` or a stopped host. Temporary transcript and builder files are removed before the network mutation starts. That record freezes the original API origin, operation ID, batch SHA-256, and a Unix-time deadline 23 hours after submission starts. Replay is rejected if `CONTENTFLOW_API_URL` differs. The same deadline is passed to every CLI submission, so an indeterminate replay cannot extend the recovery window. Recovery output advertises an enforced `reference-agent.sh --replay RECOVERY_FILE` command. Use that command before its deadline. Modifying or replacing the batch is rejected; copying or touching it cannot extend the deadline. After the deadline, reconcile remote batch state and do not replay: the API's 24-hour operation receipt may have expired, so a late submission could duplicate drafts. Builder output is bounded and must be a JSON object with 1 to 50 items before submission starts; malformed, oversized, and terminal CLI failures are not labeled replayable. The script uses the repository-built `bin/flow` by default. Set `FLOW_BIN` to an explicit executable path only when using another build.

The draft builder is an external executable selected by the agent operator. It receives one argument: a temporary file containing the exact transcript. The script disables shell tracing before reading credentials, carries them through anonymous descriptors, and re-execs Bash with an empty process environment. It restores the remaining exported settings, closes the credential descriptors, and passes credentials to only the two `flow` processes through shell environment assignments rather than arguments. This prevents helpers from recovering the original token through the parent process image. The builder runs through `examples/reference-agent-sandbox.sh`, which clears the remaining environment, denies network access, and allows reads only from the builder, transcript, and fixed operating-system runtime paths. The runner uses `sandbox-exec` on macOS and Bubblewrap on Linux, and fails closed when the required sandbox is unavailable. Set `CONTENTFLOW_AGENT_RUNNER` only to an audited runner with the same isolation contract. Builder stdout is bounded and validated; untrusted builder stderr is discarded, and failures produce only a static diagnostic. The builder must write one JSON object to standard output with 1 to 50 standalone items:

```json
{
  "items": [
    {
      "type": "x",
      "working_title": "Standalone draft 1",
      "status": "draft",
      "content": { "body": "Draft text" }
    }
  ]
}
```

The reference script performs only these ContentFlow operations:

1. `flow content transcript <id>` calls `GET /api/v1/content/<id>/transcript` and stops with exit 6 on `transcript_missing`. It never calls `show` and cannot fall back to script sections.
2. The external builder transforms the transcript into standalone draft input. No source ID or lineage field is accepted by the API.
3. `flow content batch-create --file ... --operation-id ... --json` creates all drafts atomically. A timed-out request retries the same frozen bytes and operation ID, so it cannot duplicate a committed batch. If submission still fails, the exact batch and its SHA-256-bound recovery record remain available for an idempotent replay with the same operation ID.

The included `examples/reference-draft-builder.sh` is a deterministic, non-AI example that creates 20 bounded standalone X drafts from a short transcript excerpt. Replace it with the operator's external generation step when useful.

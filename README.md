# ContentFlow

ContentFlow is a focused workspace for writing, organising, and repurposing creator content. It supports YouTube scripts, LinkedIn posts, X posts, Instagram and TikTok scripts, emails, and Substack drafts.

The approved MVP is a Go and TypeScript monorepo. A Vite-built React app is embedded in one Go binary, so the workspace and its `/api/v1` contract share one public origin. Google OAuth owner sessions and scoped API tokens protect the API. Durable content storage, uploads, and Google Cloud deployment follow in later work.

See the [MVP design](docs/mvp/design.md) for the domain model, interfaces, lifecycle, and security boundaries.

## Repository layout

```text
apps/web/             Vite and React workspace
apps/api/cmd/server/  Go server entry point
apps/api/cmd/flow/    Go CLI entry point
apps/api/internal/    Configuration, health, and HTTP packages
apps/api/web/         Embedded production web build
docs/                 Product and architecture documents
```

## Run the complete local stack

Docker with Compose and [just](https://just.systems/) are required. Start the Go application, PostgreSQL 18, and a persistent local asset volume with one command:

```bash
just dev
```

Open `http://127.0.0.1:3100`. The public listener serves the app and proxies same-origin health and API requests to a private listener using a generated secret. The private API port is available only inside the Compose network. Local proxy authentication cannot be enabled when `CONTENTFLOW_ENV=production`.

Stop the stack with:

```bash
just down
```

## Build and run the production binary

Node.js 22.13 or later and Go 1.26.6 or later are required.

```bash
npm install
npm run build
CONTENTFLOW_ENV=production \
CONTENTFLOW_ASSET_DIR=var/assets \
CONTENTFLOW_DATABASE_URL=postgres://user:password@host:5432/contentflow \
CONTENTFLOW_PUBLIC_ORIGIN=https://contentflow.example \
CONTENTFLOW_OAUTH_ISSUER=https://accounts.google.com \
CONTENTFLOW_OAUTH_CLIENT_ID=your-client-id \
CONTENTFLOW_OAUTH_CLIENT_SECRET=your-client-secret \
CONTENTFLOW_OWNER_SUBJECT=your-google-subject \
CONTENTFLOW_WORKSPACE_ID=your-workspace-id \
npm start
```

The self-contained binary listens on `http://localhost:8080`. Client-side routes fall back to the embedded `index.html`; `/api` and `/health` routes never fall through to the SPA.

Production refuses to start unless every authentication value above is present, the public origin is HTTPS, and local proxy authentication is disabled. Any authenticated public origin or OAuth issuer must also use HTTPS unless it uses an explicit loopback address for local development. Cookie security is derived from that validated origin. The OAuth redirect URI is `<public-origin>/api/v1/auth/callback`; an explicitly configured port is retained for exact provider matching. HTTPS sign-in uses OIDC `form_post` so authorization codes and state never enter request URLs or platform request logs. OAuth attempts, sessions, distributed token rate limits, and SHA-256 token hashes are stored in PostgreSQL. Session and login-attempt identifiers are stored as SHA-256 digests, never in the clear. Raw API tokens are returned only by `POST /api/v1/tokens` and are never stored.

Expired rows are removed by a cleanup pass rather than a database TTL feature. Every read already filters on `expires_at`, so cleanup reclaims space and never affects correctness.

## Use the `flow` CLI

Build the CLI and configure the public ContentFlow origin and a scoped bearer token:

```bash
go build -o bin/flow ./apps/api/cmd/flow
export CONTENTFLOW_API_URL=https://contentflow.example
export CONTENTFLOW_API_TOKEN=cf_...
```

The API URL must use HTTPS. Plaintext HTTP is accepted only for a literal loopback IP such as `http://127.0.0.1:3000`; the hostname `localhost` is rejected because it can be remapped.

Read commands require `content:read`; mutations require `content:write`. The token is read only from the environment so it does not enter command history or process arguments. The CLI never administers tokens or accesses the database or Cloud Storage.

```bash
bin/flow content list --search "launch" --type youtube --status draft
bin/flow content show 01J... --json
bin/flow content transcript 01J...
bin/flow content create --file create.json --transcript-file transcript.txt
bin/flow content update 01J... --file replacement.json --transcript-file -
bin/flow content update 01J... --file replacement.json --clear-transcript
bin/flow content batch-create --file drafts.json --json
```

Create and update files use the matching API request shape without requiring `operation_id`. Batch files contain the API `items` array. The CLI generates the operation ID, freezes the final JSON bytes, and reuses both for timeout retries. If a mutation fails, stable human and JSON errors include that operation ID. An indeterminate create, update, or batch-create also reports a mode-0600 `replay_metadata` file and a `replay_before` Unix timestamp. Retry the same request file before that deadline with `--file PATH --operation-id OPERATION_ID --replay-metadata REPLAY_METADATA`; the CLI verifies the frozen request, API origin, endpoint, operation ID, and receipt deadline before sending. When request JSON comes from standard input or another non-regular source such as a FIFO, or when transcript input is merged or cleared, the error also reports a mode-0600 `replay_file` containing the exact final request; use that path as `--file` without transcript flags. A complete 0700 recovery bundle may be copied to durable storage before replay; its fixed mode-0600 `request.json` and `metadata.json` files remain operator-owned and are not removed. After the deadline, reconcile mutation state before any new submission because API receipts expire after 24 hours. A terminal API rejection for a generated frozen request reports the same snapshot as `request_file` instead: inspect or correct it, then remove it explicitly because exact replay cannot resolve the rejection. A successful mutation removes CLI-owned recovery files but preserves operator-supplied request and metadata files. `--transcript-file -` reads the transcript from standard input; `--clear-transcript` sends an explicit empty string. They are mutually exclusive.

Every command accepts `--json`. Human transcript output is the exact transcript bytes and contains no script fallback. Machine errors are stable JSON on standard error. Exit codes are:

| Code | Meaning |
| ---: | --- |
| 0 | Success |
| 2 | CLI usage, configuration, or local input error |
| 3 | Missing or invalid authentication |
| 4 | Insufficient token scope |
| 5 | Content not found |
| 6 | Conflict, including `transcript_missing` |
| 7 | API rejected the request |
| 8 | Rate limited |
| 9 | Network or service unavailable |

See [the reference external-agent flow](docs/reference-agent-flow.md) for the transcript-only to atomic-batch sequence.

## Checks

```bash
go test ./...
npm run lint
npm test
```

`npm test` builds the embedded production binary, checks the Vite output, and runs the existing ContentFlow interaction suite.

# ContentFlow

ContentFlow is a focused workspace for writing, organising, and repurposing creator content. It supports YouTube scripts, LinkedIn posts, X posts, Instagram and TikTok scripts, emails, and Substack drafts.

The approved MVP is a Go and TypeScript monorepo. A Vite-built React app is embedded in one Go binary, so the workspace and its `/api/v1` contract share one public origin. Google OAuth owner sessions and scoped API tokens protect the API. Durable content storage, uploads, and Google Cloud deployment follow in later work.

See the [MVP design](docs/mvp/design.md) for the domain model, interfaces, lifecycle, and security boundaries.

## Repository layout

```text
apps/web/             Vite and React workspace
apps/api/cmd/server/  Go server entry point
apps/api/internal/    Configuration, health, and HTTP packages
apps/api/web/         Embedded production web build
docs/                 Product and architecture documents
```

## Run the complete local stack

Docker with Compose is required. Start the Go application, Firestore emulator, and persistent local asset volume with one command:

```bash
npm run dev
```

Open `http://localhost:3000`. The public listener serves the app and proxies same-origin health and API requests to a private listener using a generated secret. The private API port is available only inside the Compose network. Local proxy authentication cannot be enabled when `CONTENTFLOW_ENV=production`.

Stop the stack with:

```bash
npm run dev:down
```

## Build and run the production binary

Node.js 22.13 or later and Go 1.26.6 or later are required.

```bash
npm install
npm run build
CONTENTFLOW_ENV=production \
CONTENTFLOW_ASSET_DIR=var/assets \
CONTENTFLOW_GOOGLE_PROJECT_ID=your-project \
CONTENTFLOW_PUBLIC_ORIGIN=https://contentflow.example \
CONTENTFLOW_OAUTH_ISSUER=https://accounts.google.com \
CONTENTFLOW_OAUTH_CLIENT_ID=your-client-id \
CONTENTFLOW_OAUTH_CLIENT_SECRET=your-client-secret \
CONTENTFLOW_OWNER_SUBJECT=your-google-subject \
CONTENTFLOW_WORKSPACE_ID=your-workspace-id \
npm start
```

The self-contained binary listens on `http://localhost:8080`. Client-side routes fall back to the embedded `index.html`; `/api` and `/health` routes never fall through to the SPA.

Production refuses to start unless every authentication value above is present, the public origin is HTTPS, and local proxy authentication is disabled. Any authenticated public origin or OAuth issuer must also use HTTPS unless it uses an explicit loopback address for local development. Cookie security is derived from that validated origin. The OAuth redirect URI is `<public-origin>/api/v1/auth/callback`; HTTPS sign-in uses OIDC `form_post` so authorization codes and state never enter request URLs or platform request logs. OAuth attempts, sessions, distributed token rate limits, and SHA-256 token hashes are stored in Firestore. Raw API tokens are returned only by `POST /api/v1/tokens` and are never stored.

Before serving production traffic, enable managed deletion for the three expiring authentication collections:

```bash
scripts/configure-firestore-ttl.sh your-project-id
```

The script targets the default Firestore database used by the service and enables `expires_at` TTL policies for OAuth attempts, sessions, and API token rate-limit records. API token records remain until explicit revocation.

Health endpoints:

- `GET /health/live` reports whether the process is serving requests.
- `GET /health/ready` checks the writable asset directory and checks Firestore whenever authentication or the Firestore emulator is configured.

## Checks

```bash
go test ./...
npm run lint
npm test
```

`npm test` builds the embedded production binary, checks the Vite output, and runs the existing ContentFlow interaction suite.

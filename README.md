# ContentFlow

ContentFlow is a focused workspace for writing, organising, and repurposing creator content. It supports YouTube scripts, LinkedIn posts, X posts, Instagram and TikTok scripts, emails, and Substack drafts.

The approved MVP is a Go and TypeScript monorepo. A Vite-built React app is embedded in one Go binary, so the workspace and its `/api/v1` contract share one public origin. The current issue preserves the prototype data and interactions; authentication, durable content storage, uploads, and Google Cloud deployment follow in later work.

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

Node.js 22.13 or later and Go 1.24 or later are required.

```bash
npm install
npm run build
CONTENTFLOW_ENV=production CONTENTFLOW_ASSET_DIR=var/assets npm start
```

The self-contained binary listens on `http://localhost:8080`. Client-side routes fall back to the embedded `index.html`; `/api` and `/health` routes never fall through to the SPA.

Health endpoints:

- `GET /health/live` reports whether the process is serving requests.
- `GET /health/ready` checks the writable asset directory and, when `FIRESTORE_EMULATOR_HOST` is configured, Firestore connectivity.

## Checks

```bash
go test ./...
npm run lint
npm test
```

`npm test` builds the embedded production binary, checks the Vite output, and runs the existing ContentFlow interaction suite.

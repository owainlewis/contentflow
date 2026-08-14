# ContentFlow

ContentFlow is a focused UI prototype for writing, organising, and repurposing creator content. It supports YouTube scripts, LinkedIn posts, X posts, Instagram and TikTok scripts, emails, and Substack drafts.

The current version is intentionally front-end only. Content lives in local React state and resets when the page reloads.

## MVP direction

The approved MVP will run as one Go container on Cloud Run. It will serve a static TypeScript web app and the HTTP API from one origin. Firestore will store structured content, while Cloud Storage will store images, finished videos, and PDFs. Content remains available for 56 days, after which managed physical deletion follows asynchronously.

See the [MVP design](docs/mvp/design.md) for the domain model, interfaces, lifecycle, security boundaries, and acceptance criteria.

## What is included

- Searchable content library with type and status filters
- Plain-text editing for email, Substack, X, Instagram, TikTok, and LinkedIn
- Collapsible YouTube brief for topic, audience, angle, CTA, publishing details, and thumbnail
- Structured YouTube script blocks with inline section names
- Format-specific fields for email, Substack, LinkedIn, X, Instagram, and TikTok
- Session-only mock uploads for images, finished videos, and PDFs
- New-content flow for all seven formats
- Repurposing flow that creates standalone drafts without source links
- Responsive desktop and mobile layouts

## Run locally

Node.js 22.13 or later is required.

```bash
npm install
npm run dev
```

Open `http://localhost:3000`.

## Checks

```bash
npm run lint
npm test
```

`npm test` creates the production build, verifies the rendered ContentFlow workspace, and runs the interaction checks.

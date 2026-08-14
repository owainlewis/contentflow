# ContentFlow

ContentFlow is a focused UI prototype for writing, organising, and repurposing creator content. It supports YouTube scripts, LinkedIn posts, X posts, short-form reels, emails, and Substack drafts.

The current version is intentionally front-end only. Content lives in local React state and resets when the page reloads.

## What is included

- Searchable content library with type and status filters
- Plain-text editing for email, Substack, X, reels, and LinkedIn
- Collapsible YouTube brief for topic, audience, angle, CTA, publishing details, and thumbnail
- Structured YouTube script blocks with inline section names
- Format-specific fields for email, Substack, LinkedIn, X, and reels
- UI-only image and video attachment controls
- New-content flow for all six formats
- Repurposing flow that creates draft content from a source
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

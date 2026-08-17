set shell := ["bash", "-uc"]

# Show available recipes
default:
    @just --list

# Install Node dependencies
install:
    npm install

# Run the full local stack (Go API + PostgreSQL) in Docker on http://127.0.0.1:3100
dev:
    npm run dev

# Stop the local Docker stack
down:
    npm run dev:down

# Tail logs from the local Docker stack
logs:
    docker compose logs -f

# Run the Vite dev server only (frontend, no API) on http://127.0.0.1:3101
web:
    npm run dev:web

# Run the Go API directly on http://localhost:8080 against your local PostgreSQL
api:
    mkdir -p var/assets
    CONTENTFLOW_ENV=development \
    CONTENTFLOW_ADDR=:8080 \
    CONTENTFLOW_PRIVATE_ADDR=:8081 \
    CONTENTFLOW_LOCAL_PROXY_AUTH=true \
    CONTENTFLOW_ASSET_DIR=var/assets \
    CONTENTFLOW_DATABASE_URL="postgres://localhost:5432/contentflow?sslmode=disable" \
    go run ./apps/api/cmd/server

# Create the local development and test databases
db-create:
    createdb contentflow || true
    createdb contentflow_test || true

# Open a psql shell against the local development database
db:
    psql contentflow

# Build the web bundle and both Go binaries into ./bin
build:
    npm run build

# Run the built binary (requires `just build` first)
start:
    npm start

# Run the full test suite (build + node tests + vitest)
test:
    npm test

# Run frontend unit tests only
test-web:
    npx vitest run --config vitest.config.ts

# Run Go tests, including the PostgreSQL store tests
test-go:
    CONTENTFLOW_TEST_DATABASE_URL="postgres://localhost:5432/contentflow_test?sslmode=disable" go test ./...

# Lint the frontend
lint:
    npm run lint

# Type-check the frontend
typecheck:
    npx tsc --noEmit

# Format Go code and tidy modules
tidy:
    go fmt ./...
    go mod tidy

# Check the local stack is healthy
health:
    curl -fsS http://127.0.0.1:3100/health/ready && echo

# Remove build output and local containers
clean:
    rm -rf bin dist apps/api/web/dist
    docker compose down -v

FROM node:22.22-alpine AS web-build

WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY tsconfig.json ./
COPY apps/web ./apps/web
COPY apps/api/web ./apps/api/web
RUN npm run build:web

FROM golang:1.24-alpine AS go-build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY apps/api ./apps/api
COPY --from=web-build /src/apps/api/web/dist ./apps/api/web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /contentflow ./apps/api/cmd/server
RUN mkdir -p /contentflow-assets \
    && touch /contentflow-assets/.volume-init \
    && chown -R 65532:65532 /contentflow-assets

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=go-build /contentflow /contentflow
COPY --from=go-build --chown=65532:65532 /contentflow-assets/ /var/lib/contentflow/assets/
ENV CONTENTFLOW_ENV=production \
    CONTENTFLOW_ADDR=:8080 \
    CONTENTFLOW_ASSET_DIR=/tmp/contentflow-assets
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/contentflow"]

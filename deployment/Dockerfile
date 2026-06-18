# syntax=docker/dockerfile:1
#
# Unified image for the accounting-saas monolith: it serves BOTH the JSON API and
# the built Vue SPA from one origin (same-origin → no CORS). Built by Cloud Build
# with the *repo root* as the build context, so all COPY paths below are relative to
# the repo root regardless of this file living under deployment/:
#
#   gcloud builds submit . --config deployment/cloudbuild.yaml \
#       --ignore-file deployment/.gcloudignore
#
# Nothing is copied/duplicated outside the normal Docker build context.

# ---- Stage 1: build the Vue SPA (web/dist) ---------------------------------
FROM node:20-bookworm-slim AS web
WORKDIR /web
# Copy manifests first so `npm ci` is cached unless deps actually change.
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
# Force the SPA to call the API at the SAME origin (relative /api/v1), overriding
# web/.env's localhost value. .env.production.local has the highest precedence in
# Vite, so this wins over any committed .env during `vite build`.
RUN echo "VITE_API_BASE_URL=/api/v1" > .env.production.local && npm run build
# -> built static site in /web/dist

# ---- Stage 2: build the static Go binary -----------------------------------
FROM golang:1.26 AS build
WORKDIR /src
# Cache module downloads: copy go.mod/go.sum first, download, THEN copy source.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO_ENABLED=0 -> a fully static binary (pgx/gin/paseto are all pure Go), so the
# runtime image needs no libc. -tags timetzdata bakes the timezone DB into the
# binary (distroless/static has no /usr/share/zoneinfo). -trimpath + "-s -w" shrink it.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath -ldflags="-s -w" -tags timetzdata \
      -o /out/server .

# ---- Stage 3: minimal runtime ----------------------------------------------
# distroless/static ships only what a static binary needs: CA certs (outbound TLS
# to Postgres/GCS/Document AI/Gotenberg/SMTP) and a nonroot user. No shell, no
# package manager — small attack surface.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
COPY --from=web   /web/dist   /web
# The server serves the SPA from this dir when WEB_DIST_DIR is set (main.go).
ENV WEB_DIST_DIR=/web
# Documentation only; Cloud Run injects $PORT (default 8080) and the app reads it.
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]

# syntax=docker/dockerfile:1

# The browser app and the server build on the build machine's platform and
# cross-compile, so a multi-platform image needs no emulation.

# The browser app.
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# The server, with the browser app embedded.
FROM --platform=$BUILDPLATFORM golang:1.26.9 AS server
ARG VERSION=dev
ARG COMMIT=unknown
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
COPY --from=web /src/web/dist/ ./internal/ui/dist/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X github.com/ucesys/221b/server/internal/version.Version=${VERSION} -X github.com/ucesys/221b/server/internal/version.Commit=${COMMIT}" \
      -o /out/221b ./cmd/221b

# Distroless: no shell, no package manager, runs as user 65532.
FROM gcr.io/distroless/static-debian13:nonroot
LABEL org.opencontainers.image.source="https://github.com/ucesys/221b" \
      org.opencontainers.image.description="221B, a Kubernetes cluster console" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=server /out/221b /221b
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/221b"]

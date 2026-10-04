# syntax=docker/dockerfile:1

# Build on the host's native platform and cross-compile for the target, so
# multi-arch images never need slow emulation.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG TARGETOS TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/ratelimiter .

# Static binary, no shell, no package manager, non-root.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ratelimiter /ratelimiter
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/ratelimiter"]

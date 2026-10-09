# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.2 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
# Download modules first (cached layer) — fail fast if the module set is broken.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/manager ./cmd

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=build /out/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]

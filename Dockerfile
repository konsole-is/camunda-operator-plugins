# Build the binary of one plugin. PLUGIN names a directory of cmd/. The
# builder runs on the platform of the build host and cross-compiles, so a
# multi-platform build needs no emulation. The base images are pinned by
# digest, so the release publishes the same layers that its scan checked.
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:ec47ea3725fea0cc88f6de94919aef19f40f12fc1ec0e2d1b3426dbf15ecca04 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG PLUGIN
# VERSION is the version that the plugin reports. Without it the plugin
# reports 0.0.0-dev.
ARG VERSION=0.0.0-dev

WORKDIR /workspace
COPY go.mod go.sum ./
# The module proxy sometimes resets a stream, so the download gets three attempts.
RUN for attempt in 1 2 3; do \
        go mod download && break; \
        if [ "$attempt" = 3 ]; then echo "go mod download failed after 3 attempts" >&2; exit 1; fi; \
        sleep 5; \
    done

COPY . .
RUN test -n "${PLUGIN}" && test -d "cmd/${PLUGIN}" || { echo "PLUGIN must name a directory of cmd/" >&2; exit 1; }
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-X github.com/konsole-is/camunda-operator-plugins/internal/version.version=${VERSION}" \
    -o plugin "./cmd/${PLUGIN}"

FROM gcr.io/distroless/static:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
WORKDIR /
COPY --from=builder /workspace/plugin /plugin
USER 65532:65532
ENTRYPOINT ["/plugin"]

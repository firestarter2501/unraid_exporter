# syntax=docker/dockerfile:1
ARG GO_IMAGE=docker.io/library/golang:1.26-trixie

FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
ARG REVISION=unknown
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w \
        -X github.com/prometheus/common/version.Version=${VERSION} \
        -X github.com/prometheus/common/version.Revision=${REVISION} \
        -X github.com/prometheus/common/version.Branch=main" \
      -o /out/unraid_exporter ./cmd/unraid_exporter \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/mockunraid ./test/mockunraid

# Mock Unraid API used only for end-to-end tests.
FROM gcr.io/distroless/static-debian12:nonroot AS mockunraid
COPY --from=build /out/mockunraid /mockunraid
COPY internal/collector/testdata/*.json /fixtures/
EXPOSE 8080
ENTRYPOINT ["/mockunraid", "-fixtures", "/fixtures"]

# The runtime image targets Docker on Unraid; build it with `--format docker`
# (./dev.sh image does) so HEALTHCHECK is kept.
FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="unraid_exporter" \
      org.opencontainers.image.description="Prometheus exporter for the Unraid API" \
      net.unraid.docker.webui="http://[IP]:[PORT:9703]/metrics"
COPY --from=build /out/unraid_exporter /unraid_exporter
EXPOSE 9703
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/unraid_exporter", "-healthcheck"]
ENTRYPOINT ["/unraid_exporter"]

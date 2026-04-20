FROM mcr.microsoft.com/devcontainers/go:1.24 AS builder
ENV GOTOOLCHAIN=auto

WORKDIR /app

COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build cd examples/03-helm-shim && CGO_ENABLED=0 go build -o /app/helm-shim .

FROM gcr.io/distroless/static

# https://github.com/GoogleContainerTools/distroless/blob/16dc4a6a33838006fe956e4c19f049ece9c18a8d/common/variables.bzl#L18
USER 65532:65532

COPY --from=builder /app/helm-shim /bin/synthesize
COPY examples/03-helm-shim/chart /chart
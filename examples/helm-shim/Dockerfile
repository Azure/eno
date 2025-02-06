FROM mcr.microsoft.com/devcontainers/go:1.23 AS builder
WORKDIR /app

ADD go.mod .
ADD go.sum .
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" ./examples/helm-shim

FROM gcr.io/distroless/static

# https://github.com/GoogleContainerTools/distroless/blob/16dc4a6a33838006fe956e4c19f049ece9c18a8d/common/variables.bzl#L18
USER 65532:65532

COPY --from=builder /app/helm-shim /bin/synthesize
COPY examples/helm-shim/chart /chart

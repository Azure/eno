FROM mcr.microsoft.com/devcontainers/go:1.24 AS builder
ENV GOTOOLCHAIN=auto
ENV GOWORK=off
WORKDIR /app
ADD go.mod .
ADD go.sum .
RUN --mount=type=cache,target=/root/.cache/go-build go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -ldflags="-s -w" ./cmd/eno-reconciler

FROM gcr.io/distroless/static
USER 65532:65532
COPY --from=builder /app/eno-reconciler /eno-reconciler
ENTRYPOINT ["/eno-reconciler"]

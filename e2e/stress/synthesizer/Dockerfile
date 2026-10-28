FROM mcr.microsoft.com/devcontainers/go:1.25 AS builder

ENV GOTOOLCHAIN=auto
ENV GOWORK=off
WORKDIR /app

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build go mod download

COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -o /eno-stress-synthesizer ./e2e/stress/synthesizer

FROM scratch
USER 65532:65532
COPY --from=builder /eno-stress-synthesizer /bin/synthesize
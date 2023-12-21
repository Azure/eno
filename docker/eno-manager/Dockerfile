FROM golang:1.21 AS builder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -o eno-manager ./internal/manager/manager.go

FROM scratch
COPY --from=builder /app/eno-manager /eno-manager
ENTRYPOINT []
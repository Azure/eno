FROM golang:1.20 AS builder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build ./cmd/eno-wrapper

FROM scratch
COPY --from=builder /app/eno-wrapper /eno-wrapper
ENTRYPOINT ["/eno-wrapper"]

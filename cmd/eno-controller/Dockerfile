FROM golang:1.20 AS builder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build ./cmd/eno-controller

FROM scratch
COPY --from=builder /app/eno-controller /eno-controller
ENTRYPOINT ["/eno-controller"]

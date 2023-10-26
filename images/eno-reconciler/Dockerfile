FROM golang:1.20 AS builder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build ./cmd/eno-reconciler

FROM scratch
COPY --from=builder /app/eno-reconciler /eno-reconciler
ENV PATH=/bin
ENTRYPOINT ["/eno-reconciler"]

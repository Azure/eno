FROM docker.io/golang:1.21 AS builder
WORKDIR /app

ADD go.mod .
ADD go.sum .
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" ./cmd/eno-reconciler

FROM scratch
COPY --from=builder /app/eno-reconciler /eno-reconciler
ENTRYPOINT ["/eno-reconciler"]

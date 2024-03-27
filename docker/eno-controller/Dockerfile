FROM docker.io/golang:1.21 AS builder
WORKDIR /app

ADD go.mod .
ADD go.sum .
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" ./cmd/eno-controller

FROM gcr.io/distroless/static

# https://github.com/GoogleContainerTools/distroless/blob/16dc4a6a33838006fe956e4c19f049ece9c18a8d/common/variables.bzl#L18
USER 65532:65532

COPY --from=builder /app/eno-controller /eno-controller
ENTRYPOINT ["/eno-controller"]

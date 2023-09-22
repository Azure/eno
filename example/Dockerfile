FROM golang:1.20 AS builder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -o /eno-example ./example

FROM scratch
ENV PATH=/bin
COPY --from=builder /eno-example /bin/generate

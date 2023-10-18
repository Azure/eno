FROM golang:1.20 AS builder
WORKDIR /app
COPY . .
RUN curl -LO https://dl.k8s.io/release/v1.28.2/bin/linux/amd64/kubectl
RUN chmod +x kubectl
RUN ls -l
RUN CGO_ENABLED=0 go build ./cmd/eno-reconciler

FROM scratch
COPY --from=builder /app/eno-reconciler /eno-reconciler
COPY --from=builder /app/kubectl /bin/kubectl
ENV PATH=/bin
ENTRYPOINT ["/eno-reconciler"]

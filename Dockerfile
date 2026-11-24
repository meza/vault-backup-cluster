FROM golang:1.27.4@sha256:5f1b98fa02fe396ebce2a0227a0e08309e11792915b9dd2e1f71053d7e7e6666 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/vault-backup-cluster ./cmd/vault-backup-cluster

FROM alpine:3.21@sha256:81ec32e1e073303f1591af7725ca32446d44d12907c1f4e2851a95031faaec99
RUN apk add --no-cache ca-certificates curl
COPY --from=builder /out/vault-backup-cluster /vault-backup-cluster
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD curl -fsS http://127.0.0.1:8080/healthz || exit 1
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/vault-backup-cluster"]

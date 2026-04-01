FROM golang:1.25.8 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/vault-backup-cluster ./cmd/vault-backup-cluster

FROM alpine:3.21
RUN apk add --no-cache ca-certificates curl
COPY --from=builder /out/vault-backup-cluster /vault-backup-cluster
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD curl -fsS http://127.0.0.1:8080/healthz || exit 1
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/vault-backup-cluster"]

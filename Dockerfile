FROM golang:1.25.9@sha256:8a7adc288b77e9b787cd2695029eb54d10ae80571b21d44fed68d067ad0a9c96 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/vault-backup-cluster ./cmd/vault-backup-cluster

FROM alpine:3.21@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d
RUN apk add --no-cache ca-certificates curl
COPY --from=builder /out/vault-backup-cluster /vault-backup-cluster
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD curl -fsS http://127.0.0.1:8080/healthz || exit 1
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/vault-backup-cluster"]

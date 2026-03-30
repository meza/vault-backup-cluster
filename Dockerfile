FROM golang:1.25 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/vault-backup-cluster ./cmd/vault-backup-cluster

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/vault-backup-cluster /vault-backup-cluster
EXPOSE 8080
ENTRYPOINT ["/vault-backup-cluster"]

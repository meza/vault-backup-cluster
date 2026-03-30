SHELL := /bin/bash

BIN_DIR := $(CURDIR)/.bin
GO_VERSION := $(shell awk '/^go / {print $$2}' go.mod)
GO_TOOLCHAIN := go$(GO_VERSION)
MODULE := github.com/meza/vault-backup-cluster
GOLANGCI_LINT_VERSION := v2.11.4
GOVULNCHECK_VERSION := v1.1.4
GO_LICENSES_VERSION := v2.0.1

.PHONY: tools test build lint vulncheck licenses licenses-report docker-build ci

tools: $(BIN_DIR)/golangci-lint $(BIN_DIR)/govulncheck $(BIN_DIR)/go-licenses

$(BIN_DIR):
	mkdir -p $(BIN_DIR)

$(BIN_DIR)/golangci-lint: | $(BIN_DIR)
	curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(BIN_DIR) $(GOLANGCI_LINT_VERSION)

$(BIN_DIR)/govulncheck: | $(BIN_DIR)
	GOBIN=$(BIN_DIR) GOTOOLCHAIN=$(GO_TOOLCHAIN) go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

$(BIN_DIR)/go-licenses: | $(BIN_DIR)
	GOBIN=$(BIN_DIR) GOTOOLCHAIN=$(GO_TOOLCHAIN) go install github.com/google/go-licenses/v2@$(GO_LICENSES_VERSION)

test:
	go test ./...

build:
	go build -o /tmp/vault-backup-cluster ./cmd/vault-backup-cluster

lint: $(BIN_DIR)/golangci-lint
	$(BIN_DIR)/golangci-lint run

vulncheck: $(BIN_DIR)/govulncheck
	GOTOOLCHAIN=$(GO_TOOLCHAIN) $(BIN_DIR)/govulncheck ./...

licenses: $(BIN_DIR)/go-licenses
	GOTOOLCHAIN=$(GO_TOOLCHAIN) $(BIN_DIR)/go-licenses check $(MODULE)/cmd/vault-backup-cluster

licenses-report: $(BIN_DIR)/go-licenses
	GOTOOLCHAIN=$(GO_TOOLCHAIN) $(BIN_DIR)/go-licenses report $(MODULE)/cmd/vault-backup-cluster > /tmp/vault-backup-cluster-licenses.csv

docker-build:
	docker build -t vault-backup-cluster .

ci: lint test build vulncheck licenses

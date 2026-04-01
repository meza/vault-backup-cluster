ifeq ($(OS),Windows_NT)
EXEEXT := .exe
else
EXEEXT :=
endif

BIN_DIR := .bin
GOBIN_PATH := $(abspath $(BIN_DIR))
BUILD_OUTPUT := $(GOBIN_PATH)/vault-backup-cluster$(EXEEXT)
LICENSES_REPORT := $(GOBIN_PATH)/vault-backup-cluster-licenses.csv
MODULE := github.com/meza/vault-backup-cluster
GOLANGCI_LINT_VERSION := v2.11.4
GOVULNCHECK_VERSION := v1.1.4
GO_LICENSES_VERSION := v1.6.0

export GOBIN := $(GOBIN_PATH)

.PHONY: tools test build lint vulncheck licenses licenses-report docker-build ci

tools: $(GOBIN_PATH)/golangci-lint$(EXEEXT) $(GOBIN_PATH)/govulncheck$(EXEEXT) $(GOBIN_PATH)/go-licenses$(EXEEXT)

$(BIN_DIR):
	mkdir "$(BIN_DIR)"

$(GOBIN_PATH)/golangci-lint$(EXEEXT): | $(BIN_DIR)
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(GOBIN_PATH)/govulncheck$(EXEEXT): | $(BIN_DIR)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

$(GOBIN_PATH)/go-licenses$(EXEEXT): | $(BIN_DIR)
	go install github.com/google/go-licenses@$(GO_LICENSES_VERSION)

test:
	go test ./...

build: | $(BIN_DIR)
	go build -o "$(BUILD_OUTPUT)" ./cmd/vault-backup-cluster

lint: $(GOBIN_PATH)/golangci-lint$(EXEEXT)
	"$(GOBIN_PATH)/golangci-lint$(EXEEXT)" run

vulncheck: $(GOBIN_PATH)/govulncheck$(EXEEXT)
	"$(GOBIN_PATH)/govulncheck$(EXEEXT)" ./...

licenses: $(GOBIN_PATH)/go-licenses$(EXEEXT)
	"$(GOBIN_PATH)/go-licenses$(EXEEXT)" check $(MODULE)/cmd/vault-backup-cluster

licenses-report: $(GOBIN_PATH)/go-licenses$(EXEEXT) | $(BIN_DIR)
	"$(GOBIN_PATH)/go-licenses$(EXEEXT)" report $(MODULE)/cmd/vault-backup-cluster > "$(LICENSES_REPORT)"

docker-build:
	docker build -t vault-backup-cluster .

ci: lint test build vulncheck licenses

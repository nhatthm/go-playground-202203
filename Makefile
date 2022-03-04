VENDOR_DIR = vendor

GO ?= go
GOLANGCI_LINT ?= golangci-lint

.PHONY: $(VENDOR_DIR) lint test test-unit

$(VENDOR_DIR):
	@mkdir -p $(VENDOR_DIR)
	@$(GO) mod vendor
	@$(GO) mod tidy

lint:
	@$(GOLANGCI_LINT) run

test:
	@echo ">> unit test"
	@$(GO) test -gcflags=-l -coverprofile=unit.coverprofile -covermode=atomic -race -count 5 ./... -tags testcoverage

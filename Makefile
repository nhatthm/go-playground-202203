APP = playground-202203
BUILD_DIR = build

GO ?= go
GOLANGCI_LINT ?= golangci-lint
PARALLEL ?= 5

.PHONY: $(BUILD_DIR)/$(APP) $(BUILD_DIR) lint test example

lint:
	@$(GOLANGCI_LINT) run

test:
	@echo ">> unit test"
	@$(GO) test -gcflags=-l -coverprofile=unit.coverprofile -covermode=atomic -race -count 5 ./... -tags testcoverage

$(BUILD_DIR)/$(APP):
	@echo ">> build"
	@rm -f $(BUILD_DIR)/*
	@$(GO) build -o $(BUILD_DIR)/$(APP) main.go

$(BUILD_DIR): $(BUILD_DIR)/$(APP)

example: $(BUILD_DIR)/$(APP)
	$(BUILD_DIR)/$(APP) -parallel $(PARALLEL) $(shell cat examples.txt)

clean:
	@echo ">> clean"
	@rm -rf $(BUILD_DIR)

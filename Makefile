# BuildEx Makefile
# Manages building, testing, and deployment of the BuildEx CLI tool

.PHONY: help build clean test lint install deps run dev fmt vet mod check release docker

# Default target
.DEFAULT_GOAL := help

# Variables
BINARY_NAME := buildex
BUILD_DIR := ./bin
CMD_DIR := ./cmd/buildex
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags "-X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildTime=$(BUILD_TIME)"

# Go related variables
GOCMD := go
GOBUILD := $(GOCMD) build
GOCLEAN := $(GOCMD) clean
GOTEST := $(GOCMD) test
GOGET := $(GOCMD) get
GOMOD := $(GOCMD) mod
GOFMT := gofmt
GOVET := $(GOCMD) vet

# OS and Architecture detection
GOOS := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)

help: ## Show this help message
	@echo "BuildEx CLI - Docker buildx workers on EC2 Spot instances"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

deps: ## Download dependencies
	@echo "Downloading dependencies..."
	$(GOMOD) download
	$(GOMOD) tidy

build: deps ## Build the binary
	@echo "Building $(BINARY_NAME) for $(GOOS)/$(GOARCH)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)
	@echo "Binary built: $(BUILD_DIR)/$(BINARY_NAME)"

build-all: deps ## Build binaries for all platforms
	@echo "Building for all platforms..."
	@mkdir -p $(BUILD_DIR)

	# Linux AMD64
	GOOS=linux GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(CMD_DIR)

	# Linux ARM64
	GOOS=linux GOARCH=arm64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 $(CMD_DIR)

	# macOS AMD64
	GOOS=darwin GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(CMD_DIR)

	# macOS ARM64
	GOOS=darwin GOARCH=arm64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(CMD_DIR)

	# Windows AMD64
	GOOS=windows GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe $(CMD_DIR)

	@echo "All binaries built in $(BUILD_DIR)/"

clean: ## Clean build artifacts
	@echo "Cleaning build artifacts..."
	$(GOCLEAN)
	rm -rf $(BUILD_DIR)
	rm -f coverage.out

test: ## Run tests
	@echo "Running tests..."
	$(GOTEST) -v -race -coverprofile=coverage.out ./...

test-coverage: test ## Run tests with coverage report
	@echo "Generating coverage report..."
	$(GOCMD) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

lint: ## Run linter
	@echo "Running linter..."
	@which golangci-lint > /dev/null || (echo "golangci-lint not found. Install it with: curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b \$$(go env GOPATH)/bin v1.54.2" && exit 1)
	golangci-lint run ./...

fmt: ## Format code
	@echo "Formatting code..."
	$(GOFMT) -s -w .

vet: ## Run go vet
	@echo "Running go vet..."
	$(GOVET) ./...

mod: ## Update modules
	@echo "Updating modules..."
	$(GOMOD) tidy
	$(GOMOD) verify

check: fmt vet lint test ## Run all checks

install: build ## Install binary to $GOPATH/bin
	@echo "Installing $(BINARY_NAME) to $(GOPATH)/bin..."
	cp $(BUILD_DIR)/$(BINARY_NAME) $(GOPATH)/bin/

install-local: build ## Install binary to /usr/local/bin (requires sudo)
	@echo "Installing $(BINARY_NAME) to /usr/local/bin..."
	sudo cp $(BUILD_DIR)/$(BINARY_NAME) /usr/local/bin/

uninstall: ## Uninstall binary from $GOPATH/bin
	@echo "Uninstalling $(BINARY_NAME)..."
	rm -f $(GOPATH)/bin/$(BINARY_NAME)

run: build ## Run the application
	@echo "Running $(BINARY_NAME)..."
	$(BUILD_DIR)/$(BINARY_NAME)

dev: ## Run in development mode with auto-rebuild
	@echo "Running in development mode..."
	@which air > /dev/null || (echo "air not found. Install it with: go install github.com/cosmtrek/air@latest" && exit 1)
	air

docker: ## Build Docker image
	@echo "Building Docker image..."
	docker build -t $(BINARY_NAME):$(VERSION) .
	docker tag $(BINARY_NAME):$(VERSION) $(BINARY_NAME):latest

docker-run: docker ## Run Docker container
	@echo "Running Docker container..."
	docker run --rm -it \
		-v ~/.aws:/root/.aws:ro \
		-v ~/.ssh:/root/.ssh:ro \
		-v $(PWD):/workspace \
		$(BINARY_NAME):latest

release: clean check build-all ## Create a release
	@echo "Creating release $(VERSION)..."
	@mkdir -p dist

	# Create tarballs
	cd $(BUILD_DIR) && tar -czf ../dist/$(BINARY_NAME)-$(VERSION)-linux-amd64.tar.gz $(BINARY_NAME)-linux-amd64
	cd $(BUILD_DIR) && tar -czf ../dist/$(BINARY_NAME)-$(VERSION)-linux-arm64.tar.gz $(BINARY_NAME)-linux-arm64
	cd $(BUILD_DIR) && tar -czf ../dist/$(BINARY_NAME)-$(VERSION)-darwin-amd64.tar.gz $(BINARY_NAME)-darwin-amd64
	cd $(BUILD_DIR) && tar -czf ../dist/$(BINARY_NAME)-$(VERSION)-darwin-arm64.tar.gz $(BINARY_NAME)-darwin-arm64
	cd $(BUILD_DIR) && zip ../dist/$(BINARY_NAME)-$(VERSION)-windows-amd64.zip $(BINARY_NAME)-windows-amd64.exe

	# Generate checksums
	cd dist && sha256sum *.tar.gz *.zip > checksums.txt

	@echo "Release files created in dist/"
	@ls -la dist/

setup: ## Setup development environment
	@echo "Setting up development environment..."

	# Install Go tools
	go install github.com/cosmtrek/air@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

	# Download dependencies
	$(MAKE) deps

	@echo "Development environment setup complete!"

version: ## Show version information
	@echo "BuildEx CLI"
	@echo "Version: $(VERSION)"
	@echo "Commit: $(COMMIT)"
	@echo "Build Time: $(BUILD_TIME)"
	@echo "Go Version: $(shell go version)"

# Development helpers
watch: ## Watch for changes and rebuild
	@echo "Watching for changes..."
	@which entr > /dev/null || (echo "entr not found. Install it with your package manager" && exit 1)
	find . -name "*.go" | entr -r make build

serve-docs: ## Serve documentation locally
	@echo "Serving documentation on http://localhost:8080"
	@which python3 > /dev/null && cd docs && python3 -m http.server 8080 || echo "python3 not found"

generate: ## Generate code (if needed)
	@echo "Generating code..."
	$(GOCMD) generate ./...

benchmark: ## Run benchmarks
	@echo "Running benchmarks..."
	$(GOTEST) -bench=. -benchmem ./...

profile: ## Run with profiling
	@echo "Running with profiling..."
	$(BUILD_DIR)/$(BINARY_NAME) --cpuprofile=cpu.prof --memprofile=mem.prof

# CI/CD helpers
ci-test: ## Run tests in CI environment
	$(GOTEST) -v -race -coverprofile=coverage.out -covermode=atomic ./...

ci-build: ## Build in CI environment
	CGO_ENABLED=0 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)

# Cleanup helpers
clean-all: clean ## Clean everything including modules cache
	$(GOCMD) clean -modcache

reset: clean-all deps ## Reset project (clean and reinstall dependencies)
	@echo "Project reset complete!"

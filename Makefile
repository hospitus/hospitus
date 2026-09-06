# HOSPITUS Makefile

# Project info
PROJECT_NAME := hospitus
VERSION := 1.0.0
BUILD_TIME := `date -u '+%Y-%m-%d_%H:%M:%S'`
GIT_COMMIT := `git rev-parse --short HEAD 2>/dev/null || echo "unknown"`

# Go parameters
GOCMD := go
GOBUILD := $(GOCMD) build
GOCLEAN := $(GOCMD) clean
GOTEST := $(GOCMD) test
GOGET := $(GOCMD) get
GOMOD := $(GOCMD) mod
GOFMT := $(GOCMD) fmt

# Binary names
DAEMON_BINARY := hospitusd
CLI_BINARY := hospitus
DAEMON_SOURCE := ./cmd/hospitusd
CLI_SOURCE := ./cmd/hospitus-cli

# Build flags
LDFLAGS := -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME) -X main.gitCommit=$(GIT_COMMIT)
BUILDFLAGS := -buildvcs=false -ldflags "$(LDFLAGS)"

# Install paths
INSTALL_PREFIX := /usr/local
INSTALL_BIN := $(INSTALL_PREFIX)/bin
INSTALL_SYSCONF := /etc/hospitus
INSTALL_VAR := /var/lib/hospitus

# Colors for output
COLOR_RESET := \033[0m
COLOR_BOLD := \033[1m
COLOR_GREEN := \033[32m
COLOR_YELLOW := \033[33m
COLOR_BLUE := \033[34m

# Documentation paths
DOCS_SRC  := docs/book
DOCS_HTML := docs/public
DOCS_PDF  := docs/hospitus-book.pdf

.PHONY: all build build-daemon build-cli clean test test-coverage \
        test-integration test-integration-full test-integration-script test-quick \
        test-manifests \
        lint fmt vet deps deps-update install uninstall \
        build-freebsd build-linux build-darwin build-darwin-arm64 build-all \
        dev-daemon dev-cli release \
        docs docs-html docs-pdf docs-clean help

# Default target
all: clean fmt vet test build

# Build all binaries
build: build-daemon build-cli
	@printf '%b\n' "$(COLOR_GREEN)✓ Build complete$(COLOR_RESET)"

# Build daemon
build-daemon:
	@printf '%b\n' "$(COLOR_BLUE)Building $(DAEMON_BINARY)...$(COLOR_RESET)"
	$(GOBUILD) $(BUILDFLAGS) -o $(DAEMON_BINARY) $(DAEMON_SOURCE)

# Build CLI
build-cli:
	@printf '%b\n' "$(COLOR_BLUE)Building $(CLI_BINARY)...$(COLOR_RESET)"
	$(GOBUILD) $(BUILDFLAGS) -o $(CLI_BINARY) $(CLI_SOURCE)

# Clean build artifacts
clean:
	@printf '%b\n' "$(COLOR_YELLOW)Cleaning...$(COLOR_RESET)"
	$(GOCLEAN)
	rm -f $(DAEMON_BINARY) $(CLI_BINARY)
	rm -rf dist/ coverage.out coverage.html

# Run tests
test:
	@printf '%b\n' "$(COLOR_BLUE)Running tests...$(COLOR_RESET)"
	$(GOTEST) -v -short ./...

# Run tests with coverage
test-coverage:
	@printf '%b\n' "$(COLOR_BLUE)Running tests with coverage...$(COLOR_RESET)"
	$(GOTEST) -v -short -coverprofile=coverage.out -covermode=atomic ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html
	@printf '%b\n' "$(COLOR_GREEN)✓ Coverage report generated: coverage.html$(COLOR_RESET)"

# Run integration tests (requires FreeBSD with jail support)
test-integration: build
	@printf '%b\n' "$(COLOR_BLUE)Running integration tests...$(COLOR_RESET)"
	@printf '%b\n' "$(COLOR_YELLOW)Note: Requires root privileges. Run with: doas make test-integration$(COLOR_RESET)"
	$(GOTEST) -v ./test/integration/...

# Run integration tests with full suite (including long-running tests)
test-integration-full: build
	@printf '%b\n' "$(COLOR_BLUE)Running full integration test suite...$(COLOR_RESET)"
	@printf '%b\n' "$(COLOR_YELLOW)Note: Requires root privileges. Run with: doas make test-integration-full$(COLOR_RESET)"
	HOSPITUS_LONG_TESTS=1 $(GOTEST) -v ./test/integration/...

# Run integration tests via script
test-integration-script: build
	@printf '%b\n' "$(COLOR_BLUE)Running integration tests via script...$(COLOR_RESET)"
	./test/run-tests.sh

# Deploy every example manifest and run the health check it declares
test-manifests: build
	@printf '%b\n' "$(COLOR_BLUE)Deploying every example manifest...$(COLOR_RESET)"
	@printf '%b\n' "$(COLOR_YELLOW)Note: Requires root privileges. Run with: doas make test-manifests$(COLOR_RESET)"
	$(GOTEST) -v -timeout 60m -run TestExampleManifests ./test/integration/...

# Run integration tests - quick mode
test-quick: build
	@printf '%b\n' "$(COLOR_BLUE)Running quick integration tests...$(COLOR_RESET)"
	$(GOTEST) -v -run "TestImageCatalog|TestImageAlreadyExists" ./test/integration/...

# Format code
fmt:
	@printf '%b\n' "$(COLOR_BLUE)Formatting code...$(COLOR_RESET)"
	$(GOFMT) ./...

# Run go vet
vet:
	@printf '%b\n' "$(COLOR_BLUE)Running go vet...$(COLOR_RESET)"
	$(GOCMD) vet ./...

# Run linters (requires golangci-lint)
lint:
	@printf '%b\n' "$(COLOR_BLUE)Running linters...$(COLOR_RESET)"
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "$(COLOR_YELLOW)golangci-lint not installed. Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest$(COLOR_RESET)"; \
		exit 1; \
	}
	golangci-lint run ./...

# Download dependencies
deps:
	@printf '%b\n' "$(COLOR_BLUE)Downloading dependencies...$(COLOR_RESET)"
	$(GOMOD) download
	$(GOMOD) tidy

# Update dependencies
deps-update:
	@printf '%b\n' "$(COLOR_BLUE)Updating dependencies...$(COLOR_RESET)"
	$(GOGET) -u ./...
	$(GOMOD) tidy

# Install binaries
install: build
	@printf '%b\n' "$(COLOR_BLUE)Installing binaries...$(COLOR_RESET)"
	mkdir -p $(INSTALL_BIN)
	install -m 755 $(DAEMON_BINARY) $(INSTALL_BIN)/$(DAEMON_BINARY)
	install -m 755 $(CLI_BINARY) $(INSTALL_BIN)/$(CLI_BINARY)
	@printf '%b\n' "$(COLOR_BLUE)Installing rc.d service script...$(COLOR_RESET)"
	mkdir -p /usr/local/etc/rc.d
	install -m 755 etc/rc.d/hospitus /usr/local/etc/rc.d/hospitus
	@printf '%b\n' "$(COLOR_BLUE)Installing example configuration...$(COLOR_RESET)"
	mkdir -p /usr/local/etc/hospitus
	install -m 640 etc/hospitus/hospitusd.conf.example /usr/local/etc/hospitus/hospitusd.conf.example
	@printf '%b\n' "$(COLOR_YELLOW)  Example config installed to /usr/local/etc/hospitus/hospitusd.conf.example$(COLOR_RESET)"
	@printf '%b\n' "$(COLOR_YELLOW)  Copy and edit it: cp /usr/local/etc/hospitus/hospitusd.conf.example /usr/local/etc/hospitus/hospitusd.conf$(COLOR_RESET)"
	@printf '%b\n' "$(COLOR_GREEN)✓ Installed to $(INSTALL_BIN)$(COLOR_RESET)"
	@echo ""
	@echo "To enable and start the service:"
	@echo "  sysrc hospitus_enable=YES"
	@echo "  service hospitus start"

# Uninstall binaries, rc.d script, and config sample installed by `install`
uninstall:
	@printf '%b\n' "$(COLOR_YELLOW)Stopping and disabling the service...$(COLOR_RESET)"
	-service hospitus stop 2>/dev/null || true
	-sysrc -x hospitus_enable 2>/dev/null || true
	@printf '%b\n' "$(COLOR_YELLOW)Uninstalling binaries...$(COLOR_RESET)"
	rm -f $(INSTALL_BIN)/$(DAEMON_BINARY)
	rm -f $(INSTALL_BIN)/$(CLI_BINARY)
	@printf '%b\n' "$(COLOR_YELLOW)Removing rc.d service script and config sample...$(COLOR_RESET)"
	rm -f /usr/local/etc/rc.d/hospitus
	rm -f /usr/local/etc/hospitus/hospitusd.conf.example
	@printf '%b\n' "$(COLOR_GREEN)✓ Uninstalled$(COLOR_RESET)"
	@echo ""
	@echo "Your edited config and data directories are left in place. To remove them:"
	@echo "  sudo rm -rf /usr/local/etc/hospitus $(INSTALL_VAR)"

# Cross-compilation targets.
#
# Only the CLI cross-compiles. The daemon links go-sqlite3, which needs cgo, and
# a cross build without a C toolchain produces a binary that starts and then
# dies on its first query with "compiled with CGO_ENABLED=0". Build hospitusd on
# the machine it will run on, or with a cross toolchain and CGO_ENABLED=1.
build-freebsd:
	@printf '%b\n' "$(COLOR_BLUE)Building the CLI for FreeBSD/amd64...$(COLOR_RESET)"
	GOOS=freebsd GOARCH=amd64 $(GOBUILD) $(BUILDFLAGS) -o dist/freebsd-amd64/$(CLI_BINARY) $(CLI_SOURCE)

build-linux:
	@printf '%b\n' "$(COLOR_BLUE)Building the CLI for Linux/amd64...$(COLOR_RESET)"
	GOOS=linux GOARCH=amd64 $(GOBUILD) $(BUILDFLAGS) -o dist/linux-amd64/$(CLI_BINARY) $(CLI_SOURCE)

build-darwin:
	@printf '%b\n' "$(COLOR_BLUE)Building the CLI for macOS/amd64...$(COLOR_RESET)"
	GOOS=darwin GOARCH=amd64 $(GOBUILD) $(BUILDFLAGS) -o dist/darwin-amd64/$(CLI_BINARY) $(CLI_SOURCE)

build-darwin-arm64:
	@printf '%b\n' "$(COLOR_BLUE)Building the CLI for macOS/arm64...$(COLOR_RESET)"
	GOOS=darwin GOARCH=arm64 $(GOBUILD) $(BUILDFLAGS) -o dist/darwin-arm64/$(CLI_BINARY) $(CLI_SOURCE)

build-all: build-freebsd build-linux build-darwin build-darwin-arm64
	@printf '%b\n' "$(COLOR_GREEN)✓ CLI cross-compilation complete$(COLOR_RESET)"
	@printf '%b\n' "$(COLOR_YELLOW)  hospitusd is not cross-built: build it on its target platform$(COLOR_RESET)"

# Development targets
dev-daemon:
	@printf '%b\n' "$(COLOR_BLUE)Running daemon in development mode...$(COLOR_RESET)"
	$(GOCMD) run $(DAEMON_SOURCE)/main.go

dev-cli:
	@printf '%b\n' "$(COLOR_BLUE)Running CLI in development mode...$(COLOR_RESET)"
	$(GOCMD) run $(CLI_SOURCE)/main.go $(ARGS)

# Release target (create release archive)
release: clean build-all
	@printf '%b\n' "$(COLOR_BLUE)Creating release archives...$(COLOR_RESET)"
	mkdir -p dist/releases
	cd dist/freebsd-amd64 && tar -czf ../releases/$(PROJECT_NAME)-$(VERSION)-freebsd-amd64.tar.gz *
	cd dist/linux-amd64 && tar -czf ../releases/$(PROJECT_NAME)-$(VERSION)-linux-amd64.tar.gz *
	cd dist/darwin-amd64 && tar -czf ../releases/$(PROJECT_NAME)-$(VERSION)-darwin-amd64.tar.gz *
	cd dist/darwin-arm64 && tar -czf ../releases/$(PROJECT_NAME)-$(VERSION)-darwin-arm64.tar.gz *
	@printf '%b\n' "$(COLOR_GREEN)✓ Release archives created in dist/releases/$(COLOR_RESET)"

# Documentation targets

# Build HTML documentation with mdbook
docs-html:
	@printf '%b\n' "$(COLOR_BLUE)Building HTML documentation...$(COLOR_RESET)"
	@command -v mdbook >/dev/null 2>&1 || { \
		echo "$(COLOR_YELLOW)mdbook not installed. Install with: pkg install mdbook$(COLOR_RESET)"; \
		exit 1; \
	}
	mdbook build $(DOCS_SRC)
	@printf '%b\n' "$(COLOR_GREEN)✓ HTML docs: $(DOCS_HTML)/index.html$(COLOR_RESET)"

# Build PDF documentation (requires pandoc + xelatex)
# Depends on docs-html because mdbook generates print.html (single-page book)
docs-pdf: docs-html
	@printf '%b\n' "$(COLOR_BLUE)Building PDF documentation...$(COLOR_RESET)"
	@command -v pandoc >/dev/null 2>&1 || { \
		echo "$(COLOR_YELLOW)pandoc not installed. Install with: pkg install pandoc$(COLOR_RESET)"; \
		exit 1; \
	}
	@command -v xelatex >/dev/null 2>&1 || { \
		echo "$(COLOR_YELLOW)xelatex not installed. Install with: pkg install texlive-full$(COLOR_RESET)"; \
		exit 1; \
	}
	pandoc $(DOCS_HTML)/print.html \
		--pdf-engine=xelatex \
		--metadata title="The Hospitus Book" \
		--metadata author="Hospitus Contributors" \
		-V geometry:margin=1in \
		-V fontsize=11pt \
		-V documentclass=article \
		-V colorlinks=true \
		-V linkcolor=blue \
		-o $(DOCS_PDF)
	@printf '%b\n' "$(COLOR_GREEN)✓ PDF docs: $(DOCS_PDF)$(COLOR_RESET)"

# Build both HTML and PDF
docs: docs-html docs-pdf

# Remove documentation build artifacts
docs-clean:
	@printf '%b\n' "$(COLOR_YELLOW)Cleaning documentation artifacts...$(COLOR_RESET)"
	rm -rf $(DOCS_HTML) $(DOCS_PDF)
	@printf '%b\n' "$(COLOR_GREEN)✓ Documentation artifacts cleaned$(COLOR_RESET)"

# Help target
help:
	@printf '%b\n' "$(COLOR_BOLD)HOSPITUS Makefile$(COLOR_RESET)"
	@echo ""
	@printf '%b\n' "$(COLOR_BOLD)Usage:$(COLOR_RESET)"
	@echo "  make <target>"
	@echo ""
	@printf '%b\n' "$(COLOR_BOLD)Targets:$(COLOR_RESET)"
	@echo "  $(COLOR_GREEN)all$(COLOR_RESET)              - Clean, format, vet, test, and build (default)"
	@echo "  $(COLOR_GREEN)build$(COLOR_RESET)            - Build all binaries"
	@echo "  $(COLOR_GREEN)build-daemon$(COLOR_RESET)     - Build daemon only"
	@echo "  $(COLOR_GREEN)build-cli$(COLOR_RESET)        - Build CLI only"
	@echo "  $(COLOR_GREEN)clean$(COLOR_RESET)            - Remove build artifacts"
	@echo "  $(COLOR_GREEN)test$(COLOR_RESET)             - Run unit tests"
	@echo "  $(COLOR_GREEN)test-coverage$(COLOR_RESET)    - Run tests with coverage report"
	@echo "  $(COLOR_GREEN)test-integration$(COLOR_RESET) - Run integration tests"
	@echo "  $(COLOR_GREEN)fmt$(COLOR_RESET)              - Format code"
	@echo "  $(COLOR_GREEN)vet$(COLOR_RESET)              - Run go vet"
	@echo "  $(COLOR_GREEN)lint$(COLOR_RESET)             - Run linters (requires golangci-lint)"
	@echo "  $(COLOR_GREEN)deps$(COLOR_RESET)             - Download dependencies"
	@echo "  $(COLOR_GREEN)deps-update$(COLOR_RESET)      - Update dependencies"
	@echo "  $(COLOR_GREEN)install$(COLOR_RESET)          - Install binaries to $(INSTALL_BIN)"
	@echo "  $(COLOR_GREEN)uninstall$(COLOR_RESET)        - Uninstall binaries"
	@echo "  $(COLOR_GREEN)dev-daemon$(COLOR_RESET)       - Run daemon in development mode"
	@echo "  $(COLOR_GREEN)dev-cli$(COLOR_RESET)          - Run CLI in development mode (use ARGS=...)"
	@echo ""
	@printf '%b\n' "$(COLOR_BOLD)Cross-compilation:$(COLOR_RESET)"
	@echo "  $(COLOR_GREEN)build-freebsd$(COLOR_RESET)    - Build for FreeBSD/amd64"
	@echo "  $(COLOR_GREEN)build-linux$(COLOR_RESET)      - Build for Linux/amd64"
	@echo "  $(COLOR_GREEN)build-darwin$(COLOR_RESET)     - Build for macOS/amd64"
	@echo "  $(COLOR_GREEN)build-darwin-arm64$(COLOR_RESET) - Build for macOS/arm64"
	@echo "  $(COLOR_GREEN)build-all$(COLOR_RESET)        - Build for all platforms"
	@echo "  $(COLOR_GREEN)release$(COLOR_RESET)          - Create release archives"
	@echo ""
	@printf '%b\n' "$(COLOR_BOLD)Documentation:$(COLOR_RESET)"
	@echo "  $(COLOR_GREEN)docs$(COLOR_RESET)             - Build HTML + PDF documentation"
	@echo "  $(COLOR_GREEN)docs-html$(COLOR_RESET)        - Build HTML docs (requires mdbook)"
	@echo "  $(COLOR_GREEN)docs-pdf$(COLOR_RESET)         - Build PDF docs  (requires pandoc + xelatex)"
	@echo "  $(COLOR_GREEN)docs-clean$(COLOR_RESET)       - Remove docs build artifacts"
	@echo ""
	@printf '%b\n' "$(COLOR_BOLD)Examples:$(COLOR_RESET)"
	@echo "  make build"
	@echo "  make test"
	@echo "  make install"
	@echo "  make dev-cli ARGS=\"provider list\""
	@echo ""
	@printf '%b\n' "$(COLOR_BOLD)Version:$(COLOR_RESET) $(VERSION)"
	@printf '%b\n' "$(COLOR_BOLD)Git Commit:$(COLOR_RESET) $(GIT_COMMIT)"

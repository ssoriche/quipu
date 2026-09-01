# quipu Makefile

SHELL := /bin/bash

BINARY_NAME := quipu
BUILD_VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -s -w -X main.version=$(BUILD_VERSION)

.PHONY: help setup build build-all test test-coverage lint install clean release tag-release release-snapshot tidy fmt vet check

help: ## Show this help message
	@echo "quipu - git worktree and Claude Code session tracker"
	@echo ""
	@echo "Available targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

setup: ## Set up git hooks for development
	git config core.hooksPath .githooks
	@echo "Git hooks configured."

build: ## Build for current platform
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME) ./cmd/$(BINARY_NAME)

build-all: ## Build for all released platforms (linux/darwin; quipu is POSIX-only)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME)_linux_amd64 ./cmd/$(BINARY_NAME)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME)_linux_arm64 ./cmd/$(BINARY_NAME)
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME)_darwin_amd64 ./cmd/$(BINARY_NAME)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME)_darwin_arm64 ./cmd/$(BINARY_NAME)

test: ## Run tests
	go test -v -race ./...

test-coverage: ## Run tests with coverage
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

lint: ## Run linter
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed. Run: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; exit 1; }
	golangci-lint run ./...

install: build ## Install to ~/.local/bin
	cp bin/$(BINARY_NAME) $(HOME)/.local/bin/

clean: ## Clean build artifacts
	rm -rf bin/
	rm -f coverage.out coverage.html

# Release management
# Usage: make release VERSION=x.y.z
# This bumps nix/package.nix and opens a PR. After merging, run:
#   make tag-release VERSION=x.y.z
release: ## Create version bump PR (usage: make release VERSION=x.y.z)
	@if [ -z "$(VERSION)" ]; then \
		echo "Error: VERSION is required. Usage: make release VERSION=x.y.z"; \
		exit 1; \
	fi
	@if ! echo "$(VERSION)" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?(\+[A-Za-z0-9.-]+)?$$'; then \
		echo "Error: VERSION '$(VERSION)' is not a valid semver string (e.g., 1.2.3 or 1.2.3-rc.1)"; \
		exit 1; \
	fi
	@if [ -n "$$(git status --porcelain)" ]; then \
		echo "Error: Working directory is not clean. Commit or stash changes first."; \
		exit 1; \
	fi
	@CURRENT_BRANCH=$$(git rev-parse --abbrev-ref HEAD); \
	if [ "$$CURRENT_BRANCH" != "main" ]; then \
		echo "Error: Must be on main branch to release. Currently on $$CURRENT_BRANCH"; \
		echo "Run: git checkout main && git pull"; \
		exit 1; \
	fi
	@git fetch origin main
	@LOCAL=$$(git rev-parse --verify main); \
	REMOTE=$$(git rev-parse --verify origin/main); \
	if [ "$$LOCAL" != "$$REMOTE" ]; then \
		echo "Error: Local main is out of sync with origin/main. Run: git pull"; \
		exit 1; \
	fi
	@git checkout -b "release/v$(VERSION)"
	@perl -pi -e 's/^(\s*version = ")[^"]*(";)$$/$${1}$(VERSION)$${2}/' nix/package.nix
	@git add nix/package.nix
	@git commit -m "chore: bump version to $(VERSION)"
	@git push -u origin "release/v$(VERSION)"
	@printf '## Summary\n\n- Bump version to $(VERSION) in nix/package.nix\n\nAfter merging, run:\n\n    make tag-release VERSION=$(VERSION)\n' \
		| gh pr create --title "chore: bump version to $(VERSION)" --body-file -
	@echo "PR created. After merge, run: make tag-release VERSION=$(VERSION)"

# Tag a release after the version bump PR is merged.
# Pushing the tag triggers .github/workflows/release.yaml, which runs goreleaser.
tag-release: ## Tag and push a release after the version bump PR is merged (usage: make tag-release VERSION=x.y.z)
	@if [ -z "$(VERSION)" ]; then \
		echo "Error: VERSION is required. Usage: make tag-release VERSION=x.y.z"; \
		exit 1; \
	fi
	@if ! echo "$(VERSION)" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?(\+[A-Za-z0-9.-]+)?$$'; then \
		echo "Error: VERSION '$(VERSION)' is not a valid semver string (e.g., 1.2.3 or 1.2.3-rc.1)"; \
		exit 1; \
	fi
	@CURRENT_BRANCH=$$(git rev-parse --abbrev-ref HEAD); \
	if [ "$$CURRENT_BRANCH" != "main" ]; then \
		echo "Error: Must be on main branch to tag. Currently on $$CURRENT_BRANCH"; \
		echo "Run: git checkout main && git pull"; \
		exit 1; \
	fi
	@git fetch origin main
	@LOCAL=$$(git rev-parse --verify main); \
	REMOTE=$$(git rev-parse --verify origin/main); \
	if [ "$$LOCAL" != "$$REMOTE" ]; then \
		echo "Error: Local main is out of sync with origin/main. Run: git pull"; \
		exit 1; \
	fi
	@PKG_VERSION=$$(sed -n 's/^[[:space:]]*version = "\(.*\)";$$/\1/p' nix/package.nix); \
	if [ "$$PKG_VERSION" != "$(VERSION)" ]; then \
		echo "Error: nix/package.nix version ($$PKG_VERSION) does not match VERSION=$(VERSION)."; \
		echo "Did you merge and pull the version bump PR first?"; \
		exit 1; \
	fi
	@if git tag -l "v$(VERSION)" | grep -q .; then \
		echo "Error: Local tag v$(VERSION) already exists. Run: git tag -d v$(VERSION)"; \
		exit 1; \
	fi
	@if git ls-remote --tags origin "refs/tags/v$(VERSION)" | grep -q .; then \
		echo "Error: Tag v$(VERSION) already exists on origin."; \
		exit 1; \
	fi
	@git tag -a "v$(VERSION)" -m "Release v$(VERSION)"
	@git push origin "v$(VERSION)"
	@echo ""
	@echo "Tag v$(VERSION) pushed successfully!"
	@echo "The release workflow will build and publish via goreleaser. Monitor with: gh run watch"

release-snapshot: ## Create snapshot release (for testing)
	@command -v goreleaser >/dev/null 2>&1 || { echo "goreleaser not installed"; exit 1; }
	goreleaser release --snapshot --clean

tidy: ## Tidy go modules
	go mod tidy

fmt: ## Format code
	go fmt ./...

vet: ## Run go vet
	go vet ./...

check: fmt vet lint test build ## Run all checks

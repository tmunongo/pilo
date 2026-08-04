.PHONY: help build run dev fmt vet test clean docker-build version patch minor major release-patch release-minor release-major

BINARY_NAME := pilo
IMAGE_NAME := ghcr.io/tmunongo/pilo

CURRENT_VERSION := $(shell git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
VERSION_CLEAN := $(patsubst v%,%,$(CURRENT_VERSION))
MAJOR := $(word 1,$(subst ., ,$(VERSION_CLEAN)))
MINOR := $(word 2,$(subst ., ,$(VERSION_CLEAN)))
PATCH := $(word 3,$(subst ., ,$(VERSION_CLEAN)))

NEXT_PATCH := v$(MAJOR).$(MINOR).$(shell expr $(PATCH) + 1 2>/dev/null || echo 1)
NEXT_MINOR := v$(MAJOR).$(shell expr $(MINOR) + 1 2>/dev/null || echo 1).0
NEXT_MAJOR := v$(shell expr $(MAJOR) + 1 2>/dev/null || echo 1).0.0

help: ## Display available commands
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

version: ## Show current version and calculated next SemVer targets
	@echo "Current Version : $(CURRENT_VERSION)"
	@echo "Next Patch      : $(NEXT_PATCH)"
	@echo "Next Minor      : $(NEXT_MINOR)"
	@echo "Next Major      : $(NEXT_MAJOR)"

build: ## Build local Go binary
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BINARY_NAME) main.go

run: build ## Build and run locally
	PILO_PORT=8080 ./$(BINARY_NAME)

dev: ## Run with Air live-reload (auto-rebuilds on file changes)
	@command -v air >/dev/null 2>&1 || { echo "Air not found. Installing..."; go install github.com/air-verse/air@latest; }
	air -c .air.toml

test: ## Run unit tests
	go test -v ./...

fmt: ## Format all Go source files with gofmt
	gofmt -w $(shell find . -name '*.go' -not -path './.air/*')

vet: ## Run go vet for static analysis
	go vet ./...

clean: ## Clean build artifacts
	rm -f $(BINARY_NAME) pilo-arm64

docker-build: ## Build local Docker image
	docker build -t $(BINARY_NAME):latest .

patch: release-patch ## Alias for release-patch
minor: release-minor ## Alias for release-minor
major: release-major ## Alias for release-major

release-patch: ## Tag and push next patch version (vX.Y.Z -> vX.Y.Z+1)
	@echo "Bumping patch version: $(CURRENT_VERSION) -> $(NEXT_PATCH)"
	git tag -a $(NEXT_PATCH) -m "Release $(NEXT_PATCH)"
	git push origin $(NEXT_PATCH)
	@echo "Successfully tagged and pushed $(NEXT_PATCH)"

release-minor: ## Tag and push next minor version (vX.Y.Z -> vX.Y+1.0)
	@echo "Bumping minor version: $(CURRENT_VERSION) -> $(NEXT_MINOR)"
	git tag -a $(NEXT_MINOR) -m "Release $(NEXT_MINOR)"
	git push origin $(NEXT_MINOR)
	@echo "Successfully tagged and pushed $(NEXT_MINOR)"

release-major: ## Tag and push next major version (vX.Y.Z -> vX+1.0.0)
	@echo "Bumping major version: $(CURRENT_VERSION) -> $(NEXT_MAJOR)"
	git tag -a $(NEXT_MAJOR) -m "Release $(NEXT_MAJOR)"
	git push origin $(NEXT_MAJOR)
	@echo "Successfully tagged and pushed $(NEXT_MAJOR)"

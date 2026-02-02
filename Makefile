# Copyright 2026 Preferred Networks, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

SHELL := /bin/bash

IMAGE=ghcr.io/pfnet/ns-reloader
VERSION=$(shell git rev-parse HEAD)$(shell git diff --shortstat --exit-code --quiet || echo -dirty)

uname_s := $(shell uname -s)
uname_m := $(shell uname -m)
arch.x86_64 := amd64
arch.arm64 := arm64
arch.aarch64 := arm64
arch := $(arch.$(uname_m))
kernel.Linux := linux
kernel.Darwin := darwin
kernel := $(kernel.$(uname_s))

LOCALBIN ?= $(shell pwd)/build/tools

## Tool Binaries
ENVTEST_BINARY ?= $(LOCALBIN)/envtest
GCI_BINARY ?= $(LOCALBIN)/gci
GOLANGCI_LINT_BINARY = $(LOCALBIN)/golangci-lint
GOIMPORTS_BINARY = $(LOCALBIN)/goimports

## Tool Versions
GCI_VERSION ?= v0.13.7
# NOTE: Do not include the v prefix for golangci-lint
GOLANGCI_LINT_VERSION ?= 2.7.2
GOIMPORTS_VERSION ?= v0.40.0

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

.PHONY: all
all: test build docker-build ## Run tests and build.

.PHONY: build
build: ## Build binary.
	@CGO_ENABLED=0 GOOS=linux go build -o build/ns-reloader-$(kernel)-$(arch) .

.PHONY: test
test: lint unit-test ## Run tests.

clean: ## Clean up build artifacts.
	@$(RM) -f ns-reloader
	@# NOTE: envtest creates files that are not writable.
	@chmod -R +w build/
	@$(RM) -rf build/

##@ Development

.PHONY: unit-test
unit-test: envtest-install ## Run unit tests.
	@KUBEBUILDER_ASSETS="$(shell $(ENVTEST_BINARY) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -race ./...

.PHONY: lint
lint: fmt-check vet golangci-lint ## Run all linters.

.PHONY: golangci-lint
golangci-lint: golangci-lint-install ## Run golangci-lint.
	@$(GOLANGCI_LINT_BINARY) run

.PHONY: fmt
fmt: goimports-install gci-install ## Format code.
	@# Format code
	@go fmt ./...
	@# Remove unused imports 
	@$(GOIMPORTS_BINARY) -l -w .
	@# format imports by import type
	@$(GCI_BINARY) write \
		--skip-generated \
		--skip-vendor \
		-s standard \
		-s default \
		-s localmodule \
		.

.PHONY: fmt-check
fmt-check: goimports-install gci-install ## Check code is formatted.
	@unformatted="$$( \
		( \
			gofmt -l . && \
			$(GOIMPORTS_BINARY) -l . && \
			$(GCI_BINARY) list \
				--skip-generated \
				--skip-vendor \
				-s standard \
				-s default \
				-s localmodule \
				. \
		) | sort -u \
	)"; \
	if [ -n "$${unformatted}" ]; then \
		echo "The following files are not formatted. Please run 'make fmt'." >&2; \
		echo "$${unformatted}" >&2; \
		exit 1; \
	fi;

.PHONY: vet
vet: ## Run go vet against code.
	@go vet ./...

##@ Container Image

PLATFORM ?= linux/amd64
EXTRA_DOCKER_BUILD_ARGS ?= --load

.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	@docker buildx build \
		--platform $(PLATFORM) \
		-t $(IMAGE):$(VERSION) \
		--build-arg BUILD_VERSION=$(VERSION) \
		--build-arg BUILD_COMMIT=$(COMMIT) \
		--output type=docker \
		$(EXTRA_DOCKER_BUILD_ARGS) .

##@ Build Dependencies

## Location to install dependencies to
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

.PHONY: envtest-install
envtest-install: $(ENVTEST_BINARY) ## Download envtest locally.
$(ENVTEST_BINARY): $(LOCALBIN)
	$(call go-install-tool,setup-envtest,$(ENVTEST_BINARY),sigs.k8s.io/controller-runtime/tools/setup-envtest,latest)

.PHONY: golangci-lint-install
golangci-lint-install: $(GOLANGCI_LINT_BINARY) ## Download golangci-lint locally.
$(GOLANGCI_LINT_BINARY): $(LOCALBIN)
	$(call install-release,golangci-lint,$(GOLANGCI_LINT_BINARY),"https://github.com/golangci/golangci-lint/releases/download/v$(GOLANGCI_LINT_VERSION)/golangci-lint-$(GOLANGCI_LINT_VERSION)-$(kernel)-$(arch).tar.gz","golangci-lint-$(GOLANGCI_LINT_VERSION)-$(kernel)-$(arch)/golangci-lint","v$(GOLANGCI_LINT_VERSION)")

.PHONY: gci-install
gci-install: $(GCI_BINARY) ## Download gci locally.
$(GCI_BINARY): $(LOCALBIN)
	$(call go-install-tool,gci,$(GCI_BINARY),github.com/daixiang0/gci,${GCI_VERSION})

.PHONY: goimports-install
goimports-install: $(GOIMPORTS_BINARY) ## Download goimports locally.
$(GOIMPORTS_BINARY): $(LOCALBIN)
	$(call go-install-tool,goimports,$(GOIMPORTS_BINARY),golang.org/x/tools/cmd/goimports,${GOIMPORTS_VERSION})

# $1 - tool name
# $2 - target path with name of binary
# $3 - release tarball url
# $4 - path to binary inside the tarball
# $5 - version tag
define install-release
@[ -f $(2) ] || { \
set -euo pipefail; \
temp_dir=$$(mktemp -d); \
cd "$${temp_dir}"; \
echo "Downloading $(1) $(5)"; \
curl -sSLf -o release.tar.gz "$(3)"; \
tar -xzf release.tar.gz; \
mv "$${temp_dir}/$(4)" "$(2)"; \
rm -rf "$${temp_dir}"; \
}
endef

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - tool name
# $2 - target path with name of binary
# $3 - package url which can be installed
# $4 - specific version of package
define go-install-tool
@[ -f $(2) ] || { \
set -e; \
temp_dir=$$(mktemp -d); \
cd "$${temp_dir}"; \
go mod init tmp; \
echo "Downloading $(1) $(4)"; \
GOBIN="$${temp_dir}" go install $(3)@$(4); \
mv "$${temp_dir}/$(1)" "$(2)"; \
rm -rf "$${temp_dir}"; \
}
endef

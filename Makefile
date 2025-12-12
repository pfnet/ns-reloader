IMAGE=ghcr.io/pfnet/ns-reloader
VERSION=$(shell git rev-parse HEAD)$(shell git diff --shortstat --exit-code --quiet || echo -dirty)

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: all
all: test build

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: envtest lint ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -race ./...

.PHONY: lint
lint: fmt vet ## Run golangci-lint linter.
	$(GOLANGCI_LINT) run

##@ Container Image

PLATFORM ?= linux/amd64
EXTRA_BUILD_ARGS ?= --load

.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	docker buildx build --platform $(PLATFORM) -t $(IMAGE):$(VERSION) \
		--build-arg BUILD_VERSION=$(VERSION) \
		--build-arg BUILD_COMMIT=$(COMMIT) \
		$(EXTRA_BUILD_ARGS) .

##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/build/tools
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
GOLANGCI_LINT = golangci-lint
ENVTEST ?= $(LOCALBIN)/envtest

## Tool Versions
ENVTEST_K8S_VERSION ?= 1.34.0

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest locally.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,setup-envtest,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,latest)

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - tool name
# $2 - target path with name of binary
# $3 - package url which can be installed
# $4 - specific version of package
define go-install-tool
@[ -f $(2) ] || { \
set -e; \
TMP_DIR=$$(mktemp -d); \
cd $$TMP_DIR; \
go mod init tmp; \
echo "Downloading $(1) $(4)"; \
GOBIN=$$TMP_DIR go install $(3)@$(4); \
mv $$TMP_DIR/$(1) $(2); \
rm -rf $$TMP_DIR; \
}
endef

# Image URL to use all building/pushing image targets
IMG ?= controller:latest

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code (including e2e-tagged sources).
	go vet -tags=e2e ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -race -coverprofile cover.out

.PHONY: govulncheck
govulncheck: ## Check Go module graph against the Go vuln database.
	@command -v govulncheck >/dev/null 2>&1 || go install golang.org/x/vuln/cmd/govulncheck@latest
	govulncheck ./...

# TODO(user): To use a different vendor for e2e tests, modify the setup under 'tests/e2e'.
# The default setup assumes Kind is pre-installed and builds/loads the Manager Docker image locally.
# CertManager is installed by default; skip with:
# - CERT_MANAGER_INSTALL_SKIP=true
KIND_CLUSTER ?= paddock-test-e2e

.PHONY: setup-test-e2e
setup-test-e2e: ## Set up a Kind cluster for e2e tests if it does not exist
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@case "$$($(KIND) get clusters)" in \
		*"$(KIND_CLUSTER)"*) \
			echo "Kind cluster '$(KIND_CLUSTER)' already exists. Skipping creation." ;; \
		*) \
			echo "Creating Kind cluster '$(KIND_CLUSTER)' with Cilium CNI..."; \
			$(KIND) create cluster --name $(KIND_CLUSTER) --config hack/kind-with-cilium.yaml ;; \
	esac
	@CLUSTER_NAME=$(KIND_CLUSTER) hack/install-cilium.sh

.PHONY: test-e2e
test-e2e: setup-test-e2e ginkgo manifests generate fmt vet ## Run the e2e tests. Expected an isolated environment using Kind.
	@# Process-level parallelism comes from the ginkgo CLI (--procs=N
	@# or -p for auto). go test exposes only ginkgo's per-worker flags
	@# (parallel.process / parallel.total) which the ginkgo CLI sets
	@# itself when fanning out workers — running through go test would
	@# always be serial. The CLI is pinned via go.mod's ginkgo/v2
	@# version and installed under bin/ by `make ginkgo`.
	@#
	@# Timeout: 30m matches the per-spec budget; the CI workflow caps
	@#          the job at 45m for headroom around build+deploy.
	@# GINKGO_PROCS controls process-level parallelism. Default is -p
	@#               (= GOMAXPROCS-1). Set GINKGO_PROCS=1 to force
	@#               serial execution — the always-available debugging
	@#               fallback.
	@# LABELS filters specs by Ginkgo Label, e.g. LABELS=smoke for the
	@#               happy-path specs, LABELS=broker, LABELS=hostile,
	@#               LABELS=interactive.
	@# FAIL_FAST=1 → opt-in fast iteration: stop on the first failing
	@#               spec instead of running them all.
	@# KEEP_CLUSTER=1 → skip cluster teardown so a subsequent run
	@#                  reuses it; pair with KEEP_E2E_RUN=1 for full
	@#                  tenant-state retention.
	KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) "$(GINKGO)" -tags=e2e -v --timeout=30m \
		$(if $(GINKGO_PROCS),--procs=$(GINKGO_PROCS),-p) \
		$(if $(LABELS),--label-filter=$(LABELS),) \
		$(if $(FAIL_FAST),--fail-fast,) \
		./test/e2e/
	$(if $(KEEP_CLUSTER),@echo "KEEP_CLUSTER=1: leaving Kind cluster intact",$(MAKE) cleanup-test-e2e)

.PHONY: cleanup-test-e2e
cleanup-test-e2e: ## Tear down the Kind cluster used for e2e tests
	@$(KIND) delete cluster --name $(KIND_CLUSTER)

.PHONY: kind-up
kind-up: ## Create local dev Kind cluster and install cert-manager.
	hack/kind-up.sh

.PHONY: kind-load
kind-load: ## Load all paddock-*:dev images into the local dev Kind cluster (paddock-dev).
	@for img in paddock-manager:dev paddock-broker:dev paddock-proxy:dev paddock-iptables-init:dev \
	           paddock-echo:dev paddock-runtime-echo:dev \
	           paddock-claude-code:dev paddock-runtime-claude-code:dev; do \
		$(KIND) load docker-image --name paddock-dev "$$img"; \
	done

.PHONY: kind-down
kind-down: ## Delete the local dev Kind cluster.
	hack/kind-down.sh

.PHONY: tilt-up
tilt-up: ## Run Tilt against the local dev Kind cluster.
	tilt up

.PHONY: tilt-down
tilt-down: ## Tear down Tilt-managed resources.
	tilt down

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	"$(GOLANGCI_LINT)" run

.PHONY: hooks-install
hooks-install: ## Install the git pre-commit hook (gofmt + vet + lint).
	@mkdir -p .git/hooks
	@ln -sf ../../hack/pre-commit.sh .git/hooks/pre-commit
	@chmod +x hack/pre-commit.sh
	@echo "pre-commit hook installed → .git/hooks/pre-commit → hack/pre-commit.sh"
	@echo "(bypass with 'git commit --no-verify' — CI still runs the same checks.)"

.PHONY: hooks-uninstall
hooks-uninstall: ## Remove the git pre-commit hook.
	@rm -f .git/hooks/pre-commit
	@echo "pre-commit hook removed"

.PHONY: pre-commit
pre-commit: ## Run the pre-commit checks manually (same as the hook).
	hack/pre-commit.sh

.PHONY: update-reader-image-digest
update-reader-image-digest: ## Refresh the busybox digest pinned in internal/cli/logs.go (manual).
	@./hack/update-reader-image-digest.sh

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	"$(GOLANGCI_LINT)" run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	"$(GOLANGCI_LINT)" config verify

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: cli
cli: fmt vet ## Build the kubectl-paddock plugin binary.
	go build -o bin/kubectl-paddock ./cmd/kubectl-paddock
	@echo "built bin/kubectl-paddock — place on PATH to use as 'kubectl paddock'"

.PHONY: paddock-tui
paddock-tui: fmt vet ## Build the paddock-tui binary.
	go build -o bin/paddock-tui ./cmd/paddock-tui
	@echo "built bin/paddock-tui — interactive multi-session TUI for Paddock"

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

##@ Reference images

ECHO_IMG ?= paddock-echo:dev
EVIL_ECHO_IMG ?= paddock-evil-echo:dev
HARNESS_SUPERVISOR_IMG ?= paddock-harness-supervisor:dev
CLAUDE_CODE_IMG ?= paddock-claude-code:dev
RUNTIME_CLAUDE_CODE_IMG ?= paddock-runtime-claude-code:dev
RUNTIME_ECHO_IMG ?= paddock-runtime-echo:dev
BROKER_IMG ?= paddock-broker:dev
PROXY_IMG ?= paddock-proxy:dev
IPTABLES_INIT_IMG ?= paddock-iptables-init:dev
E2E_EGRESS_IMG ?= paddock-e2e-egress:dev

.PHONY: image-echo
image-echo: ## Build the paddock-echo harness image, skipping if source hash matches.
	@hash=$$(hack/image-hash.sh echo); \
	tag="paddock-echo:dev-$$hash"; \
	if $(CONTAINER_TOOL) image inspect $$tag >/dev/null 2>&1; then \
		echo "image-echo: source hash $$hash unchanged, retagging :dev-$$hash to :dev"; \
		$(CONTAINER_TOOL) tag $$tag $(ECHO_IMG); \
	else \
		echo "image-echo: building $(ECHO_IMG) (hash $$hash)"; \
		$(CONTAINER_TOOL) build -t $(ECHO_IMG) -t $$tag -f images/harness-echo/Dockerfile .; \
	fi

.PHONY: image-evil-echo
image-evil-echo: ## Build the paddock-evil-echo hostile harness image (test-only), skipping if source hash matches.
	@hash=$$(hack/image-hash.sh evil-echo); \
	tag="paddock-evil-echo:dev-$$hash"; \
	if $(CONTAINER_TOOL) image inspect $$tag >/dev/null 2>&1; then \
		echo "image-evil-echo: source hash $$hash unchanged, retagging :dev-$$hash to :dev"; \
		$(CONTAINER_TOOL) tag $$tag $(EVIL_ECHO_IMG); \
	else \
		echo "image-evil-echo: building $(EVIL_ECHO_IMG) (hash $$hash)"; \
		$(CONTAINER_TOOL) build -t $(EVIL_ECHO_IMG) -t $$tag -f images/evil-echo/Dockerfile .; \
	fi

.PHONY: image-harness-supervisor
image-harness-supervisor: ## Build the paddock-harness-supervisor image (bridges UDS to harness CLI stdio), skipping if source hash matches.
	@hash=$$(hack/image-hash.sh harness-supervisor); \
	tag="paddock-harness-supervisor:dev-$$hash"; \
	if $(CONTAINER_TOOL) image inspect $$tag >/dev/null 2>&1; then \
		echo "image-harness-supervisor: source hash $$hash unchanged, retagging :dev-$$hash to :dev"; \
		$(CONTAINER_TOOL) tag $$tag $(HARNESS_SUPERVISOR_IMG); \
	else \
		echo "image-harness-supervisor: building $(HARNESS_SUPERVISOR_IMG) (hash $$hash)"; \
		$(CONTAINER_TOOL) build -t $(HARNESS_SUPERVISOR_IMG) -t $$tag -f images/harness-supervisor/Dockerfile .; \
	fi

.PHONY: image-claude-code
image-claude-code: ## Build the paddock-claude-code demo harness image (wraps Anthropic's claude CLI), skipping if source hash matches.
	@hash=$$(hack/image-hash.sh claude-code); \
	tag="paddock-claude-code:dev-$$hash"; \
	if $(CONTAINER_TOOL) image inspect $$tag >/dev/null 2>&1; then \
		echo "image-claude-code: source hash $$hash unchanged, retagging :dev-$$hash to :dev"; \
		$(CONTAINER_TOOL) tag $$tag $(CLAUDE_CODE_IMG); \
	else \
		echo "image-claude-code: building $(CLAUDE_CODE_IMG) (hash $$hash)"; \
		$(CONTAINER_TOOL) build -t $(CLAUDE_CODE_IMG) -t $$tag -f images/harness-claude-code/Dockerfile .; \
	fi

.PHONY: image-claude-code-fake
image-claude-code-fake: ## Build the fake-claude harness image (e2e only — no install, no API call), skipping if source hash matches.
	@hash=$$(hack/image-hash.sh claude-code-fake); \
	tag="paddock-claude-code-fake:dev-$$hash"; \
	if $(CONTAINER_TOOL) image inspect $$tag >/dev/null 2>&1; then \
		echo "image-claude-code-fake: source hash $$hash unchanged, retagging :dev-$$hash to :dev"; \
		$(CONTAINER_TOOL) tag $$tag paddock-claude-code-fake:dev; \
	else \
		echo "image-claude-code-fake: building paddock-claude-code-fake:dev (hash $$hash)"; \
		$(CONTAINER_TOOL) build -t paddock-claude-code-fake:dev -t $$tag -f images/harness-claude-code-fake/Dockerfile .; \
	fi

.PHONY: image-runtime-claude-code
image-runtime-claude-code: ## Build the paddock-runtime-claude-code sidecar image, skipping if source hash matches.
	@hash=$$(hack/image-hash.sh runtime-claude-code); \
	tag="paddock-runtime-claude-code:dev-$$hash"; \
	if $(CONTAINER_TOOL) image inspect $$tag >/dev/null 2>&1; then \
		echo "image-runtime-claude-code: source hash $$hash unchanged, retagging :dev-$$hash to :dev"; \
		$(CONTAINER_TOOL) tag $$tag $(RUNTIME_CLAUDE_CODE_IMG); \
	else \
		echo "image-runtime-claude-code: building $(RUNTIME_CLAUDE_CODE_IMG) (hash $$hash)"; \
		$(CONTAINER_TOOL) build -t $(RUNTIME_CLAUDE_CODE_IMG) -t $$tag -f images/runtime-claude-code/Dockerfile .; \
	fi

.PHONY: image-runtime-echo
image-runtime-echo: ## Build the paddock-runtime-echo sidecar image, skipping if source hash matches.
	@hash=$$(hack/image-hash.sh runtime-echo); \
	tag="paddock-runtime-echo:dev-$$hash"; \
	if $(CONTAINER_TOOL) image inspect $$tag >/dev/null 2>&1; then \
		echo "image-runtime-echo: source hash $$hash unchanged, retagging :dev-$$hash to :dev"; \
		$(CONTAINER_TOOL) tag $$tag $(RUNTIME_ECHO_IMG); \
	else \
		echo "image-runtime-echo: building $(RUNTIME_ECHO_IMG) (hash $$hash)"; \
		$(CONTAINER_TOOL) build -t $(RUNTIME_ECHO_IMG) -t $$tag -f images/runtime-echo/Dockerfile .; \
	fi

.PHONY: image-broker
image-broker: ## Build the paddock-broker image, skipping if source hash matches.
	@hash=$$(hack/image-hash.sh broker); \
	tag="paddock-broker:dev-$$hash"; \
	if $(CONTAINER_TOOL) image inspect $$tag >/dev/null 2>&1; then \
		echo "image-broker: source hash $$hash unchanged, retagging :dev-$$hash to :dev"; \
		$(CONTAINER_TOOL) tag $$tag $(BROKER_IMG); \
	else \
		echo "image-broker: building $(BROKER_IMG) (hash $$hash)"; \
		$(CONTAINER_TOOL) build -t $(BROKER_IMG) -t $$tag -f images/broker/Dockerfile .; \
	fi

.PHONY: image-proxy
image-proxy: ## Build the paddock-proxy sidecar image, skipping if source hash matches.
	@hash=$$(hack/image-hash.sh proxy); \
	tag="paddock-proxy:dev-$$hash"; \
	if $(CONTAINER_TOOL) image inspect $$tag >/dev/null 2>&1; then \
		echo "image-proxy: source hash $$hash unchanged, retagging :dev-$$hash to :dev"; \
		$(CONTAINER_TOOL) tag $$tag $(PROXY_IMG); \
	else \
		echo "image-proxy: building $(PROXY_IMG) (hash $$hash)"; \
		$(CONTAINER_TOOL) build -t $(PROXY_IMG) -t $$tag -f images/proxy/Dockerfile .; \
	fi

.PHONY: image-iptables-init
image-iptables-init: ## Build the paddock-iptables-init image (NET_ADMIN init container), skipping if source hash matches.
	@hash=$$(hack/image-hash.sh iptables-init); \
	tag="paddock-iptables-init:dev-$$hash"; \
	if $(CONTAINER_TOOL) image inspect $$tag >/dev/null 2>&1; then \
		echo "image-iptables-init: source hash $$hash unchanged, retagging :dev-$$hash to :dev"; \
		$(CONTAINER_TOOL) tag $$tag $(IPTABLES_INIT_IMG); \
	else \
		echo "image-iptables-init: building $(IPTABLES_INIT_IMG) (hash $$hash)"; \
		$(CONTAINER_TOOL) build -t $(IPTABLES_INIT_IMG) -t $$tag -f images/iptables-init/Dockerfile .; \
	fi

.PHONY: image-e2e-egress
image-e2e-egress: ## Build the paddock-e2e-egress harness (e2e-only probe tool), skipping if source hash matches.
	@hash=$$(hack/image-hash.sh e2e-egress); \
	tag="paddock-e2e-egress:dev-$$hash"; \
	if $(CONTAINER_TOOL) image inspect $$tag >/dev/null 2>&1; then \
		echo "image-e2e-egress: source hash $$hash unchanged, retagging :dev-$$hash to :dev"; \
		$(CONTAINER_TOOL) tag $$tag $(E2E_EGRESS_IMG); \
	else \
		echo "image-e2e-egress: building $(E2E_EGRESS_IMG) (hash $$hash)"; \
		$(CONTAINER_TOOL) build -t $(E2E_EGRESS_IMG) -t $$tag images/harness-e2e-egress; \
	fi

.PHONY: images
images: image-echo image-runtime-echo image-harness-supervisor image-claude-code image-runtime-claude-code image-broker image-proxy image-iptables-init image-evil-echo ## Build all reference images.

.PHONY: trivy-images
trivy-images: ## Run trivy on all Paddock images. Fails on HIGH/CRITICAL.
	@command -v trivy >/dev/null 2>&1 || { echo "Install trivy: https://aquasecurity.github.io/trivy/"; exit 1; }
	@for img in paddock-manager:dev paddock-broker:dev paddock-proxy:dev paddock-iptables-init:dev \
	            paddock-echo:dev paddock-runtime-echo:dev \
	            paddock-claude-code:dev paddock-runtime-claude-code:dev; do \
	  echo "==> trivy on $$img"; \
	  trivy image --severity HIGH,CRITICAL --exit-code 1 --ignorefile .trivyignore "$$img" || exit 1; \
	done

.PHONY: kube-lint
kube-lint: ## Run kube-linter on the Helm chart and config samples.
	@command -v kube-linter >/dev/null 2>&1 || { echo "Install kube-linter: https://docs.kubelinter.io/"; exit 1; }
	helm template paddock charts/paddock --namespace paddock-system | kube-linter lint --config .kube-linter.yaml -
	kube-linter lint --config .kube-linter.yaml config/samples/

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name paddock-builder
	$(CONTAINER_TOOL) buildx use paddock-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm paddock-builder
	rm Dockerfile.cross

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default > dist/install.yaml

.PHONY: helm-chart
helm-chart: manifests generate kustomize ## Regenerate charts/paddock/ from the kustomize overlay.
	hack/gen-helm-chart.sh

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	@# Server-side apply: the template CRDs embed PodTemplateSpec and
	@# exceed the 262 KiB last-applied-configuration annotation limit
	@# on client-side apply.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" apply --server-side=true --force-conflicts -f -; else echo "No CRDs to install; skipping."; fi

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -; else echo "No CRDs to delete; skipping."; fi

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	@# Server-side apply: the embedded PodTemplateSpec in the template
	@# CRDs exceeds the client-side last-applied annotation limit.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" apply --server-side=true --force-conflicts -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
KUBECTL ?= kubectl
KIND ?= kind
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GINKGO ?= $(LOCALBIN)/ginkgo
GINKGO_VERSION ?= $(call gomodver,github.com/onsi/ginkgo/v2)

## Tool Versions
KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.20.1

#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?([0-9]+)\.([0-9]+).*/release-\1.\2/')

#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

GOLANGCI_LINT_VERSION ?= v2.11.4
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

.PHONY: ginkgo
ginkgo: $(GINKGO) ## Download ginkgo CLI locally if necessary; pinned to go.mod's ginkgo/v2.
$(GINKGO): $(LOCALBIN)
	$(call go-install-tool,$(GINKGO),github.com/onsi/ginkgo/v2/ginkgo,$(GINKGO_VERSION))

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef

define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef

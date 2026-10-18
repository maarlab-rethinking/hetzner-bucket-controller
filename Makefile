IMG ?= ghcr.io/maarlab-rethinking/hetzner-bucket-controller:dev

# Tool versions. Binaries are installed once into ./bin, suffixed with their version, so
# a bump re-installs and nothing is rebuilt on every invocation.
CONTROLLER_TOOLS_VERSION ?= v0.23.0
GOLANGCI_LINT_VERSION ?= v2.14.0
SETUP_ENVTEST_VERSION ?= v0.25.2
GOVULNCHECK_VERSION ?= v1.9.0
CRD_REF_DOCS_VERSION ?= v0.3.0
# Kubernetes version of the envtest control plane (kube-apiserver + etcd).
ENVTEST_K8S_VERSION ?= 1.37.0

LOCALBIN := $(CURDIR)/bin
CONTROLLER_GEN := $(LOCALBIN)/controller-gen-$(CONTROLLER_TOOLS_VERSION)
GOLANGCI_LINT := $(LOCALBIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)
SETUP_ENVTEST := $(LOCALBIN)/setup-envtest-$(SETUP_ENVTEST_VERSION)
GOVULNCHECK := $(LOCALBIN)/govulncheck-$(GOVULNCHECK_VERSION)
CRD_REF_DOCS := $(LOCALBIN)/crd-ref-docs-$(CRD_REF_DOCS_VERSION)

CHART := charts/hetzner-bucket-controller
GENDIR := .gen
CRD_FILE := objectstorage.maarlab.dev_buckets.yaml

.PHONY: all
all: build

##@ Development

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: generate
generate: $(CONTROLLER_GEN) ## deepcopy
	$(CONTROLLER_GEN) object:headerFile=/dev/null paths="./api/..."

.PHONY: manifests
manifests: $(CONTROLLER_GEN) ## CRD + RBAC ClusterRole -> chart templates (generated; do not hand-edit)
	@rm -rf $(GENDIR) && mkdir -p $(GENDIR)
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:dir=$(GENDIR)
	$(CONTROLLER_GEN) rbac:roleName=manager-role paths="./internal/..." output:rbac:dir=$(GENDIR)
	@test -s $(GENDIR)/$(CRD_FILE) || { \
	  echo 'make manifests: controller-gen produced no CRD. Check the markers in api/.' >&2; exit 1; }
	@# controller-gen collects +kubebuilder:rbac only from package-level comments; markers
	@# on a declaration's doc comment are ignored and it exits 0 having written nothing.
	@# Without this guard the chart would ship an EMPTY ClusterRole.
	@test -s $(GENDIR)/role.yaml || { \
	  echo 'make manifests: controller-gen produced no role.yaml. The +kubebuilder:rbac markers must' >&2; \
	  echo 'be a floating comment block, separated from the declaration below by a blank line.' >&2; \
	  exit 1; }
	@mkdir -p config/crd
	@sed -e '/^---$$/d' $(GENDIR)/$(CRD_FILE) > config/crd/$(CRD_FILE)
	@{ \
	  echo '{{- /* Generated from api/v1alpha1 by "make manifests"; do not edit. */ -}}'; \
	  echo '{{- if .Values.installCRD }}'; \
	  sed -e '/^---$$/d' $(GENDIR)/$(CRD_FILE); \
	  echo '{{- end }}'; \
	} > $(CHART)/templates/crd.yaml
	@{ \
	  echo '{{- /* Generated from +kubebuilder:rbac markers by "make manifests"; do not edit. */ -}}'; \
	  echo '{{- if .Values.rbac.create }}'; \
	  sed -e '/^---$$/d' \
	      -e '/^  creationTimestamp: null$$/d' \
	      -e 's|^  name: manager-role$$|  name: {{ include "hetzner-bucket-controller.fullname" . }}|' \
	      $(GENDIR)/role.yaml; \
	  echo '{{- end }}'; \
	} > $(CHART)/templates/clusterrole.yaml
	@rm -rf $(GENDIR)

.PHONY: api-docs
api-docs: $(CRD_REF_DOCS) ## docs/api.md from the Go types (generated; do not hand-edit)
	$(CRD_REF_DOCS) --source-path=./api/v1alpha1 --config=hack/crd-ref-docs.yaml --renderer=markdown --output-path=docs/api.md

.PHONY: fmt
fmt:
	gofmt -w api cmd internal test

.PHONY: vet
vet:
	go vet ./...

.PHONY: lint
lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run ./...

.PHONY: helm-lint
helm-lint:
	helm lint $(CHART)
	helm template hbc $(CHART) -n object-storage > /dev/null

.PHONY: vulncheck
vulncheck: $(GOVULNCHECK)
	$(GOVULNCHECK) ./...

##@ Test

# Unit tests (fake S3) + envtest (real kube-apiserver and etcd, fake S3). The MinIO
# integration test is behind the "integration" build tag: see test-integration.
.PHONY: test
test: $(SETUP_ENVTEST)
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
	  go test -race -cover ./...

# Runs the S3 client and the whole reconciler against a real S3 server (MinIO, with
# Object Lock) started by testcontainers. Needs a Docker-compatible daemon.
.PHONY: test-integration
test-integration: $(SETUP_ENVTEST)
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
	  go test -race -tags integration -count=1 ./test/integration/...

##@ Build

.PHONY: build
build: fmt
	CGO_ENABLED=0 go build -o bin/manager ./cmd

.PHONY: docker
docker:
	docker build -t $(IMG) .

##@ Tools

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

$(CONTROLLER_GEN): | $(LOCALBIN)
	$(call go-install-tool,$@,sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

$(GOLANGCI_LINT): | $(LOCALBIN)
	$(call go-install-tool,$@,github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

$(SETUP_ENVTEST): | $(LOCALBIN)
	$(call go-install-tool,$@,sigs.k8s.io/controller-runtime/tools/setup-envtest,$(SETUP_ENVTEST_VERSION))

$(GOVULNCHECK): | $(LOCALBIN)
	$(call go-install-tool,$@,golang.org/x/vuln/cmd/govulncheck,$(GOVULNCHECK_VERSION))

$(CRD_REF_DOCS): | $(LOCALBIN)
	$(call go-install-tool,$@,github.com/elastic/crd-ref-docs,$(CRD_REF_DOCS_VERSION))

# go-install-tool installs $(2)@$(3) into ./bin and renames it to $(1).
define go-install-tool
@set -e; tmp=$$(mktemp -d); \
  echo "Installing $(2)@$(3)"; \
  GOBIN=$$tmp go install $(2)@$(3); \
  mv $$tmp/$$(basename $(2)) $(1); rm -rf $$tmp
endef

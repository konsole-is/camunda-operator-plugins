# Every plugin is a directory of cmd/. Each one has a chart in charts/<plugin>
# and an image ghcr.io/konsole-is/camunda-operator-plugins/<plugin>.
PLUGINS := $(sort $(notdir $(patsubst %/,%,$(wildcard cmd/*/))))

# VERSION is the version that the binaries report. A release sets it from the
# tag. Without it a build reports 0.0.0-dev.
VERSION ?= 0.0.0-dev
VERSION_PACKAGE := github.com/konsole-is/camunda-operator-plugins/internal/version
VERSION_LDFLAGS = -X $(VERSION_PACKAGE).version=$(VERSION)

# Setting SHELL to bash allows bash commands to be executed by recipes.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: fmt
fmt: golangci-lint ## Format the code with the formatters of .golangci.yml.
	"$(GOLANGCI_LINT)" fmt

.PHONY: vet
vet: ## Run go vet against the code.
	go vet ./...

.PHONY: tidy
tidy: ## Run go mod tidy.
	go mod tidy

.PHONY: test
test: fmt vet setup-envtest envtest-crds ## Run the unit tests and the envtest suites.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" \
	ENVTEST_CRDS="$(ENVTEST_CRDS_DIR)" \
		go test $$(go list ./... | grep -v /test/e2e) -coverprofile cover.out

.PHONY: test-api-ref
test-api-ref: ## Check that a pseudo-version and a tag of the api module map to the operator ref of their CRD bases.
	@got="$$($(MAKE) -s print-api-ref API_VERSION=v0.2.1-0.20261015084104-bb2edca2c2db)"; \
		[ "$$got" = bb2edca2c2db ] || { echo "pseudo-version maps to '$$got', want bb2edca2c2db" >&2; exit 1; }
	@got="$$($(MAKE) -s print-api-ref API_VERSION=v0.0.0-20261015084104-bb2edca2c2db)"; \
		[ "$$got" = bb2edca2c2db ] || { echo "untagged pseudo-version maps to '$$got', want bb2edca2c2db" >&2; exit 1; }
	@got="$$($(MAKE) -s print-api-ref API_VERSION=v0.3.0)"; \
		[ "$$got" = api/v0.3.0 ] || { echo "tag maps to '$$got', want api/v0.3.0" >&2; exit 1; }
	@got="$$($(MAKE) -s print-api-ref API_VERSION=v0.3.0-rc.1)"; \
		[ "$$got" = api/v0.3.0-rc.1 ] || { echo "prerelease tag maps to '$$got', want api/v0.3.0-rc.1" >&2; exit 1; }

.PHONY: chart-verify
chart-verify: ## Lint and render the chart of each plugin, and check the serve rule of CONTRIBUTING.md.
	@for p in $(PLUGINS); do \
		test -f "charts/$$p/Chart.yaml" || { echo "cmd/$$p has no chart charts/$$p" >&2; exit 1; }; \
		$(HELM) lint "charts/$$p" || exit 1; \
		$(HELM) template verify "charts/$$p" --namespace $(CHART_NAMESPACE) \
			| go run ./hack/chartcheck -values "charts/$$p/values.yaml" -namespace $(CHART_NAMESPACE) || exit 1; \
		$(HELM) template verify "charts/$$p" --namespace $(CHART_NAMESPACE) -f hack/chartcheck/testdata/two-registrations.yaml \
			| go run ./hack/chartcheck -values hack/chartcheck/testdata/two-registrations.yaml -namespace $(CHART_NAMESPACE) || exit 1; \
		err="$$($(HELM) template verify "charts/$$p" --set registrations=null 2>&1 >/dev/null || true)"; \
		case "$$err" in *"registrations must name at least one registration"*) ;; *) \
			echo "charts/$$p renders without a registration" >&2; exit 1 ;; \
		esac; \
		echo "  charts/$$p refuses to render without a registration"; \
		$(MAKE) -s chart-names-differ CHART="charts/$$p" A="team-a plugin" B="team a-plugin" || exit 1; \
		$(MAKE) -s chart-names-differ CHART="charts/$$p" \
			A="$$(printf 'n%.0s' $$(seq 60))a plugin" B="$$(printf 'n%.0s' $$(seq 60))b plugin" || exit 1; \
	done

# chart-names-differ checks that two releases, A and B as "<namespace> <release>",
# render different names for the cluster-scoped objects of CHART.
.PHONY: chart-names-differ
chart-names-differ:
	@names() { $(HELM) template "$$2" "$(CHART)" --namespace "$$1" \
		| awk '/^kind: ClusterRole(Binding)?$$/ { k = 1 } k && /^  name:/ { print $$2; k = 0 }'; }; \
	a="$$(names $(A))"; b="$$(names $(B))"; \
	if [ -z "$$a" ] || [ -n "$$(comm -12 <(printf '%s\n' $$a | sort) <(printf '%s\n' $$b | sort))" ]; then \
		echo "$(CHART): the releases '$(A)' and '$(B)' share a cluster-scoped name: $$a" >&2; exit 1; \
	fi; \
	echo "  $(CHART): '$(A)' and '$(B)' render different cluster-scoped names"

# GOMOD_FILE is the go.mod that check-published-api reads. test-published-api
# sets it to a copy that requires the root module of the operator.
GOMOD_FILE ?= go.mod

.PHONY: check-published-api
check-published-api: ## Check that go.mod does not require the root module of the operator, which is not a published API.
	@required="$$(go mod edit -json "$(GOMOD_FILE)" | jq '[.Require[]?.Path] | any(. == "github.com/konsole-is/camunda-operator")')"; \
	if [ "$$required" != false ]; then \
		echo "$(GOMOD_FILE) requires github.com/konsole-is/camunda-operator, which is not a published API of the operator" >&2; \
		exit 1; \
	fi

.PHONY: test-published-api
test-published-api: ## Check that check-published-api refuses a go.mod that requires the root module of the operator.
	@tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
		cp go.mod "$$tmp/go.mod"; \
		go mod edit -require=github.com/konsole-is/camunda-operator@v0.2.0 "$$tmp/go.mod"; \
		err="$$($(MAKE) -s check-published-api GOMOD_FILE="$$tmp/go.mod" 2>&1 > /dev/null || true)"; \
		case "$$err" in *"requires github.com/konsole-is/camunda-operator, which is not a published API"*) ;; *) \
			echo "check-published-api does not refuse a go.mod that requires the root module of the operator: $$err" >&2; \
			exit 1 ;; \
		esac

.PHONY: list-plugins
list-plugins: ## Print the plugins, every directory of cmd/, as a JSON list.
	@printf '%s\n' $(PLUGINS) | jq -R . | jq -cs .

.PHONY: lint
lint: golangci-lint check-published-api ## Run golangci-lint and check that go.mod requires only the published API.
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint and apply its fixes.
	"$(GOLANGCI_LINT)" fmt && "$(GOLANGCI_LINT)" run --fix

.PHONY: lint-renovate
lint-renovate: ## Verify renovate.json5 with the validator of RENOVATE_VERSION. Needs npx.
	npx --yes --package renovate@$(RENOVATE_VERSION) renovate-config-validator --strict renovate.json5

.PHONY: lint-config
lint-config: golangci-lint golangci-lint-schema ## Verify the golangci-lint configuration.
	"$(GOLANGCI_LINT)" config verify --schema "$(GOLANGCI_LINT_SCHEMA)"

.PHONY: lint-terraform
lint-terraform: ## Check the format of each Terraform configuration under terraform/ and validate it, without its backend.
	@set -e; for dir in terraform/*/; do \
		echo "$${dir}"; \
		terraform -chdir="$${dir}" fmt -check -diff; \
		terraform -chdir="$${dir}" init -backend=false -input=false >/dev/null; \
		terraform -chdir="$${dir}" validate; \
	done

.PHONY: lint-workflows
lint-workflows: ## Check the GitHub workflows with actionlint.
	go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

.PHONY: vulncheck
vulncheck: govulncheck ## Fail on a known vulnerability that the code can reach. Run it with the Go of .tool-versions.
	"$(GOVULNCHECK)" ./...

.PHONY: test-version-stamp
test-version-stamp: VERSION = 0.3.0
test-version-stamp: ## Check that a build with VERSION reports it, and that the Dockerfile stamps it too.
	WANT_VERSION=$(VERSION) go test -count=1 -ldflags "$(VERSION_LDFLAGS)" \
		-run '^TestVersionReportsStampedTag$$' ./internal/version
	@grep -qF -- '-X $(VERSION_PACKAGE).version=$${VERSION}' Dockerfile || \
		{ echo "Dockerfile does not stamp $(VERSION_PACKAGE).version from ARG VERSION" >&2; exit 1; }

##@ Build

.PHONY: build
build: fmt vet ## Build the binary of each plugin into bin/<plugin>.
	@for p in $(PLUGINS); do go build -ldflags "$(VERSION_LDFLAGS)" -o "bin/$$p" "./cmd/$$p" || exit 1; done

##@ Images

CONTAINER_TOOL ?= docker
PLUGIN ?= gateway-access
IMAGE_REPOSITORY ?= ghcr.io/konsole-is/camunda-operator-plugins
IMG ?= $(IMAGE_REPOSITORY)/$(PLUGIN):$(VERSION)
PLATFORMS ?= linux/amd64,linux/arm64
# BUILDX_OUTPUT is --push for a release, or --load to scan one platform.
BUILDX_OUTPUT ?= --push

.PHONY: docker-build
docker-build: ## Build the image of PLUGIN as IMG.
	$(CONTAINER_TOOL) build --build-arg PLUGIN=$(PLUGIN) --build-arg VERSION=$(VERSION) -t $(IMG) .

.PHONY: docker-buildx
docker-buildx: ## Build the image of PLUGIN as IMG for PLATFORMS, and push or load it by BUILDX_OUTPUT.
	$(CONTAINER_TOOL) buildx build $(BUILDX_OUTPUT) --platform=$(PLATFORMS) \
		--build-arg PLUGIN=$(PLUGIN) --build-arg VERSION=$(VERSION) -t $(IMG) .

##@ Kind

# The kind targets run in CI: they create a kind cluster and fill it. Never
# point them at a cluster that you use.
KIND_CLUSTER ?= camunda-operator-plugins
# OPERATOR_DIR is a checkout of the operator at API_REF. The kind cluster runs
# the operator built from it, so it matches the api module of go.mod.
OPERATOR_DIR ?= $(LOCALBIN)/camunda-operator
OPERATOR_REPO ?= https://github.com/konsole-is/camunda-operator.git
# CONFORMANCE_REPORT is the JUnit report of conformance-kind.
CONFORMANCE_REPORT ?= $(LOCALBIN)/conformance/$(PLUGIN).xml
# renovate: datasource=github-releases depName=envoyproxy/gateway
ENVOY_GATEWAY_VERSION ?= v1.9.2
# renovate: datasource=docker depName=rustfs/rustfs
RUSTFS_VERSION ?= 1.0.1

.PHONY: operator-source
operator-source: ## Check out the operator at API_REF into OPERATOR_DIR.
	API_REF="$(API_REF)" OPERATOR_REPO="$(OPERATOR_REPO)" test/kind/operator-source.sh "$(OPERATOR_DIR)"

.PHONY: conformance-kind
conformance-kind: ## Create a kind cluster with the operator and PLUGIN, and run the conformance suite against PLUGIN. CI only.
	KIND_CLUSTER="$(KIND_CLUSTER)" OPERATOR_DIR="$(OPERATOR_DIR)" CHART_NAMESPACE="$(CHART_NAMESPACE)" \
	REPORT="$(CONFORMANCE_REPORT)" GATEWAY_API_VERSION="$(GATEWAY_API_VERSION)" \
	ENVOY_GATEWAY_VERSION="$(ENVOY_GATEWAY_VERSION)" RUSTFS_VERSION="$(RUSTFS_VERSION)" \
		test/conformance/run.sh "$(PLUGIN)"

##@ Dependencies

LOCALBIN ?= $(shell pwd)/bin

# HELM is the helm of .tool-versions.
HELM ?= helm
CHART_NAMESPACE ?= camunda-operator-plugins
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GOVULNCHECK = $(LOCALBIN)/govulncheck
# golangci-lint fetches its schema with a 2 s timeout and no retry, so
# lint-config downloads it with retries and passes it in.
GOLANGCI_LINT_SCHEMA = $(LOCALBIN)/golangci.$(basename $(GOLANGCI_LINT_VERSION)).jsonschema.json

GOLANGCI_LINT_VERSION ?= v2.8.0
# RENOVATE_VERSION is the Renovate release whose validator lint-renovate runs
# against renovate.json5.
# renovate: datasource=npm depName=renovate
RENOVATE_VERSION ?= 44.140.0
# renovate: datasource=go depName=golang.org/x/vuln
GOVULNCHECK_VERSION ?= v1.8.0
# renovate: datasource=go depName=github.com/rhysd/actionlint
ACTIONLINT_VERSION ?= v1.7.12

ENVTEST ?= $(LOCALBIN)/setup-envtest

# ENVTEST_VERSION is the release branch of controller-runtime that setup-envtest comes from.
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?([0-9]+)\.([0-9]+).*/release-\1.\2/')
# ENVTEST_K8S_VERSION is the Kubernetes minor of the envtest binaries, from k8s.io/api.
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

API_MODULE := github.com/konsole-is/camunda-operator/api
# API_VERSION is the version of the api module in go.mod.
API_VERSION ?= $(shell go list -m -f '{{.Version}}' $(API_MODULE))
# API_REF is the operator commit or tag whose CRD bases match API_VERSION: the
# 12-character commit of a pseudo-version, or api/<version> for a tag. A
# Renovate update of the api module moves the CRDs of envtest with it.
API_REF = $(shell printf '%s\n' '$(API_VERSION)' | \
  sed -E -e 's/^v[0-9]+\.[0-9]+\.[0-9]+-.*[0-9]{14}-([0-9a-f]{12})$$/\1/' -e t -e 's|^|api/|')
# GATEWAY_API_VERSION is the Gateway API release that go.mod pins. Its
# standard-channel CRDs load into envtest beside those of the operator.
GATEWAY_API_VERSION ?= $(call gomodver,sigs.k8s.io/gateway-api)
# One directory for each pair of versions holds one set of CRDs, so a bump of
# either never loads two copies of a CRD.
ENVTEST_CRDS_DIR = $(LOCALBIN)/crds/$(subst /,_,$(API_REF))-gateway-api-$(GATEWAY_API_VERSION)
# ENVTEST_CRD_FILES are the CRD bases of the operator that the plugins read.
# A plugin that reads a core.camunda.io kind adds its file here.
ENVTEST_CRD_FILES = \
	core.camunda.io_camundaclusters.yaml \
	core.camunda.io_camundamanagementclusters.yaml \
	core.camunda.io_camundaoptimizes.yaml \
	core.camunda.io_objectstorageconfigs.yaml \
	plugins.camunda.io_grants.yaml \
	plugins.camunda.io_pluginregistrations.yaml \
	plugins.camunda.io_pluginrequests.yaml \
	plugins.camunda.io_pluginsections.yaml

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for envtest in the local bin directory.
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path > /dev/null

.PHONY: envtest
envtest: | $(LOCALBIN) ## Download setup-envtest locally if necessary.
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: envtest-crds
envtest-crds: ## Download the CRD bases of the operator ref that the api module names, and the Gateway API CRDs, into bin/crds/.
	@mkdir -p "$(ENVTEST_CRDS_DIR)"
	@for f in $(ENVTEST_CRD_FILES); do \
		if [ ! -s "$(ENVTEST_CRDS_DIR)/$$f" ]; then \
			curl -fsSL --retry 5 --retry-all-errors --retry-delay 2 --max-time 60 -o "$(ENVTEST_CRDS_DIR)/$$f.tmp" \
				"https://raw.githubusercontent.com/konsole-is/camunda-operator/$(API_REF)/config/crd/bases/$$f" && \
				mv -f "$(ENVTEST_CRDS_DIR)/$$f.tmp" "$(ENVTEST_CRDS_DIR)/$$f" || exit 1; \
		fi; \
	done
	@f="$(ENVTEST_CRDS_DIR)/gateway-api-standard-install.yaml"; \
	if [ ! -s "$$f" ]; then \
		curl -fsSL --retry 5 --retry-all-errors --retry-delay 2 --max-time 60 -o "$$f.tmp" \
			"https://github.com/kubernetes-sigs/gateway-api/releases/download/$(GATEWAY_API_VERSION)/standard-install.yaml" && \
			mv -f "$$f.tmp" "$$f"; \
	fi

.PHONY: print-api-ref
print-api-ref: ## Print the operator ref whose CRD bases envtest loads.
	@echo "$(API_REF)"

# govulncheck cannot load code that needs a newer Go than the Go that built it, so a change of Go builds it again.
.PHONY: govulncheck
govulncheck: | $(LOCALBIN) ## Download govulncheck locally if necessary.
	@want="$(GOVULNCHECK_VERSION) $$(go env GOVERSION)"; \
	[ -x "$(GOVULNCHECK)" ] && [ "$$(cat "$(GOVULNCHECK).stamp" 2>/dev/null)" = "$$want" ] || { \
		set -e; \
		echo "Downloading golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)"; \
		GOBIN="$(LOCALBIN)" go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION); \
		echo "$$want" > "$(GOVULNCHECK).stamp"; \
	}

.PHONY: golangci-lint-schema
golangci-lint-schema: $(GOLANGCI_LINT_SCHEMA) ## Download the golangci-lint JSON schema locally if necessary.
$(GOLANGCI_LINT_SCHEMA): | $(LOCALBIN)
	curl -fsSL --retry 5 --retry-all-errors --retry-delay 2 --max-time 60 \
		-o "$@.tmp" "https://golangci-lint.run/jsonschema/$(notdir $@)" && mv -f "$@.tmp" "$@"

# The custom golangci-lint holds the plugins of .custom-gcl.yml, and it cannot
# load code that needs a newer Go than the Go that built it. A change of
# either, or of GOLANGCI_LINT_VERSION, builds it again.
.PHONY: golangci-lint
golangci-lint: | $(LOCALBIN) ## Build golangci-lint with the plugins of .custom-gcl.yml if necessary.
	@want="$(GOLANGCI_LINT_VERSION) $$(go env GOVERSION) $$(sha256sum .custom-gcl.yml | cut -d' ' -f1)"; \
	[ -x "$(GOLANGCI_LINT)" ] && [ "$$(cat "$(GOLANGCI_LINT).stamp" 2>/dev/null)" = "$$want" ] || { \
		set -e; \
		echo "Building golangci-lint $(GOLANGCI_LINT_VERSION) with the plugins of .custom-gcl.yml"; \
		rm -f "$(GOLANGCI_LINT)"; \
		GOBIN="$(LOCALBIN)" go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION); \
		"$(GOLANGCI_LINT)" custom --version "$(GOLANGCI_LINT_VERSION)" --destination "$(LOCALBIN)" --name golangci-lint-custom; \
		mv -f "$(LOCALBIN)/golangci-lint-custom" "$(GOLANGCI_LINT)"; \
		echo "$$want" > "$(GOLANGCI_LINT).stamp"; \
	}

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

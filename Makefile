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

.PHONY: lint
lint: golangci-lint ## Run golangci-lint.
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint and apply its fixes.
	"$(GOLANGCI_LINT)" fmt && "$(GOLANGCI_LINT)" run --fix

.PHONY: lint-config
lint-config: golangci-lint golangci-lint-schema ## Verify the golangci-lint configuration.
	"$(GOLANGCI_LINT)" config verify --schema "$(GOLANGCI_LINT_SCHEMA)"

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

##@ Dependencies

LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GOVULNCHECK = $(LOCALBIN)/govulncheck
# golangci-lint fetches its schema with a 2 s timeout and no retry, so
# lint-config downloads it with retries and passes it in.
GOLANGCI_LINT_SCHEMA = $(LOCALBIN)/golangci.$(basename $(GOLANGCI_LINT_VERSION)).jsonschema.json

GOLANGCI_LINT_VERSION ?= v2.8.0
# renovate: datasource=go depName=golang.org/x/vuln
GOVULNCHECK_VERSION ?= v1.8.0
# renovate: datasource=go depName=github.com/rhysd/actionlint
ACTIONLINT_VERSION ?= v1.7.12

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

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))
	@test -f .custom-gcl.yml && { \
		echo "Building custom golangci-lint with plugins..." && \
		$(GOLANGCI_LINT) custom --destination $(LOCALBIN) --name golangci-lint-custom && \
		mv -f $(LOCALBIN)/golangci-lint-custom $(GOLANGCI_LINT); \
	} || true

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

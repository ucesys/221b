# Build, test and run 221B. `make help` lists the targets.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
IMAGE   ?= ghcr.io/ucesys/221b
TAG     ?= $(VERSION)
GO      ?= go
NPM     ?= npm

# The simulated cluster for dev-sim and e2e-sim.
SIM_NODES ?= 40
SIM_PODS  ?= 900

PKG     := github.com/ucesys/221b/server/internal/version
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT)
UI_DIST := server/internal/ui/dist

.DEFAULT_GOAL := build
.PHONY: help build web ui server sim test test-go test-web lint lint-go lint-web licenses image chart-lint dev-sim e2e-sim clean

help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-11s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ui server sim ## Build the browser app and both binaries into bin/

web/node_modules: web/package-lock.json
	cd web && $(NPM) ci
	@touch $@

web: web/node_modules ## Build the browser app into web/dist
	cd web && $(NPM) run build

ui: web ## Copy the browser app into the server, which embeds it
	find $(UI_DIST) -mindepth 1 ! -name .gitkeep -delete
	cp -R web/dist/. $(UI_DIST)/

server: ## Build bin/221b with whatever is in server/internal/ui/dist
	cd server && CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o ../bin/221b ./cmd/221b

sim: ## Build bin/221b-sim, the simulated API server for development and tests
	cd server && CGO_ENABLED=0 $(GO) build -trimpath -o ../bin/221b-sim ./cmd/221b-sim

test: test-go test-web ## Run the Go and browser unit tests

test-go:
	cd server && $(GO) test -race ./...

test-web: web/node_modules
	cd web && $(NPM) test

lint: lint-go lint-web ## Check formatting, vet, types and lint

lint-go:
	@out="$$(cd server && gofmt -l cmd internal)"; \
	if [ -n "$$out" ]; then echo "Run gofmt on:"; echo "$$out"; exit 1; fi
	cd server && $(GO) vet ./...

lint-web: web/node_modules
	cd web && $(NPM) run typecheck && $(NPM) run lint

licenses: web/node_modules ## Check that every dependency's license is allowed
	cd web && $(NPM) run licenses
	cd server && $(GO) run github.com/google/go-licenses/v2@v2.0.1 check ./... \
		--disallowed_types=forbidden,restricted,unknown --ignore github.com/ucesys/221b

image: ## Build the container image as $(IMAGE):$(TAG)
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(IMAGE):$(TAG) .

chart-lint: ## Lint and render the Helm chart
	helm lint deploy/helm/221b --strict --set clusterName=ci
	helm template ci deploy/helm/221b --set clusterName=ci > /dev/null

dev-sim: build ## Run the console on http://127.0.0.1:8080 against a simulated cluster
	@bin/221b-sim -nodes $(SIM_NODES) -pods $(SIM_PODS) -churn 5s & sim=$$!; \
	trap 'kill $$sim 2>/dev/null' EXIT INT TERM; \
	bin/221b -api-server http://127.0.0.1:7443 -cluster-name simulated -log-format text

e2e-sim: build ## Run the browser end-to-end tests against a simulated cluster
	@bin/221b-sim -nodes $(SIM_NODES) -pods $(SIM_PODS) & sim=$$!; \
	bin/221b -api-server http://127.0.0.1:7443 -cluster-name simulated -health-interval 2s -metrics-interval 2s -log-format text 2> bin/e2e-server.log & srv=$$!; \
	trap 'kill $$sim $$srv 2>/dev/null' EXIT INT TERM; \
	for i in $$(seq 50); do curl -fs http://127.0.0.1:8080/readyz >/dev/null && break; sleep 0.2; done; \
	cd web && E2E_MODE=sim E2E_SIM_URL=http://127.0.0.1:7443 $(NPM) run e2e

clean: ## Remove build output
	rm -rf bin web/dist
	find $(UI_DIST) -mindepth 1 ! -name .gitkeep -delete

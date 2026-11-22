# Build, test and run 221B. `make help` lists the targets.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
IMAGE   ?= ghcr.io/ucesys/221b
TAG     ?= $(VERSION)
GO      ?= go
NPM     ?= npm
# The PostgreSQL the tests and the simulator's console run against; CI runs
# it pinned to a digest (hack/base-images.env, POSTGRES_IMAGE).
POSTGRES_IMAGE ?= postgres:18

# The simulated cluster for dev-sim and e2e-sim.
SIM_NODES ?= 40
SIM_PODS  ?= 900
# A fake SMTP server for Settings › Email (EM-10): host 127.0.0.1, port
# 7525, security none; its messages at http://127.0.0.1:7443/fake-smtp/messages.
SIM_SMTP  ?= -fake-smtp 127.0.0.1:7525

PKG     := github.com/ucesys/221b/server/internal/version
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT)
UI_DIST := server/internal/ui/dist

.DEFAULT_GOAL := build
.PHONY: help build web ui server sim cli cli-release test test-go test-go-store test-web lint lint-go lint-web licenses image image-postgres postgres-lock postgres-packages postgres-rebuild image-postgres-wolfi postgres-wolfi-lock postgres-wolfi-packages postgres-wolfi-rebuild test-db chart-lint dev-sim e2e-sim screenshots sherlock-eval clean

help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-11s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ui server sim cli ## Build the browser app and the binaries into bin/

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

cli: ## Build bin/221b-cli, the 221b command people run on their computers (port forward)
	cd server && CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o ../bin/221b-cli ./cmd/221b-cli

cli-release: ## Build the 221b command for macOS, Linux and Windows into bin/cli/
	@for p in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64; do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "221b-$$os-$$arch$$ext"; \
		(cd server && CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o ../bin/cli/221b-$$os-$$arch$$ext ./cmd/221b-cli) || exit 1; \
	done

sim: ## Build bin/221b-sim, the simulated API server for development and tests
	cd server && CGO_ENABLED=0 $(GO) build -trimpath -o ../bin/221b-sim ./cmd/221b-sim

test: test-go test-web ## Run the Go and browser unit tests

test-go:
	cd server && $(GO) test -race -timeout 30m ./...

test-go-store: ## Run the tests of the packages whose records have moved with those records in the database TEST_DATABASE names
	cd server && TEST_STORE=database $(GO) test -race ./internal/ai/ ./internal/jobs/ ./internal/casework/ ./internal/cards/ ./internal/fixes/ ./internal/lessons/ ./internal/knowledge/ ./internal/issues/ ./internal/credentials/ ./internal/apps/... ./internal/clusters/... ./internal/watson/
	cd server && TEST_STORE=database $(GO) test -race -timeout 30m ./internal/api/

test-web: web/node_modules
	cd web && $(NPM) test

lint: lint-go lint-web ## Check formatting, vet, types and lint

lint-go:
	@out="$$(cd server && gofmt -l cmd internal)"; \
	if [ -n "$$out" ]; then echo "Run gofmt on:"; echo "$$out"; exit 1; fi
	cd server && $(GO) vet ./...
	hack/check-prompt-sources.sh
	hack/check-db-backup.sh

lint-web: web/node_modules
	cd web && $(NPM) run typecheck && $(NPM) run lint && $(NPM) run format:check

licenses: web/node_modules ## Check that every dependency's license is allowed
	cd web && $(NPM) run licenses
	cd server && $(GO) run github.com/google/go-licenses/v2@v2.0.1 check ./... \
		--disallowed_types=forbidden,restricted,unknown --ignore github.com/ucesys/221b

# Both images need BuildKit (--platform=$BUILDPLATFORM, COPY --chmod),
# which Docker before 23 does not use unless it is told to.
image: ## Build the container image as $(IMAGE):$(TAG)
	DOCKER_BUILDKIT=1 docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(IMAGE):$(TAG) .

# The bundled database's image (deploy/postgres, docs/database.md, "The
# image"): its base is assembled by apko from Wolfi's packages, so it is
# built by hack/wolfi-postgres.sh, which says what each step reads
# (PLATFORM, DOCKER, WOLFI_PACKAGES_REF, WOLFI_PACKAGES_KEY, COSIGN_KEY...).
image-postgres: ## Build the bundled database's image for PLATFORM (linux/amd64) as $(IMAGE)-postgres:$(TAG)
	DB_IMAGE=$(IMAGE)-postgres:$(TAG) hack/wolfi-postgres.sh image

postgres-lock: ## Take Wolfi's current packages for the database's image: a new apko.lock.json, and its base's digest in the Dockerfile
	hack/wolfi-postgres.sh lock
	hack/wolfi-postgres.sh pin

postgres-packages: ## Copy the database image's locked packages to WOLFI_PACKAGES_REF, their index signed with WOLFI_PACKAGES_KEY, the copy with cosign
	hack/wolfi-postgres.sh packages
	hack/wolfi-postgres.sh push

postgres-rebuild: ## Rebuild the database's image from the copy WOLFI_PACKAGES (a release's tag or a digest) alone
	DB_IMAGE=$(IMAGE)-postgres:$(TAG) hack/wolfi-postgres.sh rebuild $(WOLFI_PACKAGES)

# The names of the Wolfi variant's targets, from before it was the image.
image-postgres-wolfi: image-postgres
postgres-wolfi-lock: postgres-lock
postgres-wolfi-packages: postgres-packages
postgres-wolfi-rebuild: postgres-rebuild

test-db: ## Run the Go tests with those that need PostgreSQL, against a throwaway one in Docker, or the one TEST_DATABASE names (host:port)
	@if [ -z "$$TEST_DATABASE" ]; then \
	  docker run -d --rm --name 221b-test-db -e POSTGRES_HOST_AUTH_METHOD=trust -p 127.0.0.1:55432:5432 $(POSTGRES_IMAGE) > /dev/null; \
	  trap 'docker rm -f 221b-test-db > /dev/null' EXIT; export TEST_DATABASE=127.0.0.1:55432; \
	  until docker exec 221b-test-db pg_isready -q; do sleep 1; done; \
	fi; cd server && $(GO) test -race ./...

chart-lint: ## Lint and render the Helm chart
	helm lint deploy/helm/221b --strict --set clusterName=ci
	helm template ci deploy/helm/221b --set clusterName=ci > /dev/null

# The simulated identity provider (P3-01): the simulator serves it at
# /oidc, with admin@corp.example, dev@corp.example and viewer@corp.example
# (password: password); admins are the platform-admins group.
SIM_OIDC = -oidc-issuer http://127.0.0.1:7443/oidc
SIM_AUTH = -auth-mode oidc -oidc-issuer http://127.0.0.1:7443/oidc -oidc-client-id 221b -oidc-client-secret 221b-secret -oidc-provider-name "Simulated provider" -admin-groups platform-admins -access-ttl 10s

# The console keeps its records in PostgreSQL and does not run without it
# (DB-69). dev-sim, e2e-sim and screenshots start one in Docker for the run
# ($(POSTGRES_IMAGE) on 127.0.0.1:$(DEV_DB_PORT), thrown away at the end), unless
# DEV_DATABASE names one as host:port that lets the user postgres in
# without a password; the database DEV_DB_NAME there must be empty, or
# this console's from an earlier run.
DEV_DATABASE ?=
DEV_DB_PORT ?= 55433
DEV_DB_NAME ?= postgres
# Starts the database (sets db, the container to stop, and dbaddr).
DEV_DB_START = if [ -z "$(DEV_DATABASE)" ]; then \
	  db=221b-dev-db-$$$$; dbaddr=127.0.0.1:$(DEV_DB_PORT); \
	  docker run -d --rm --name $$db -e POSTGRES_HOST_AUTH_METHOD=trust -p $$dbaddr:5432 $(POSTGRES_IMAGE) > /dev/null || exit 1; \
	  until docker exec $$db pg_isready -q -h 127.0.0.1; do sleep 1; done; \
	else db=; dbaddr=$(DEV_DATABASE); fi
# The container was started with --rm: stopping it removes it.
DEV_DB_STOP = if [ -n "$$db" ]; then docker stop -t 1 $$db > /dev/null; fi
DEV_DB_FLAGS = -db-host $${dbaddr%:*} -db-port $${dbaddr\#\#*:} -db-user postgres -db-name $(DEV_DB_NAME) -db-sslmode disable

# The records are in that database; the connections' credentials and the
# other secrets are Secrets of the simulated cluster, in the namespace
# 221b (P3-07), named after the release the console is told it is
# (DB-97). The simulator serves the ConfigMaps of a release before 0.13,
# empty: the first start copies nothing from them and deletes them.
SIM_STORE = -release-namespace 221b -release-fullname k8s-console-221b

# Sherlock (Phase 5) talks to the simulator's fake Claude API; an
# administrator turns it on in Settings with any key of the right shape.
SIM_SHERLOCK = -sherlock-api-url http://127.0.0.1:7443/claude
# Watson (Phase 6) may change the simulated cluster once someone approves.
SIM_WATSON = -local-mode write -toolbox-config hack/e2e-toolbox.json

dev-sim: build ## Run the console on http://127.0.0.1:8080 against a simulated cluster and a throwaway PostgreSQL in Docker (or DEV_DATABASE), signing in through the simulated provider
	@$(DEV_DB_START); \
	trap '$(DEV_DB_STOP)' EXIT INT TERM; \
	bin/221b-sim -nodes $(SIM_NODES) -pods $(SIM_PODS) -churn 5s $(SIM_OIDC) $(SIM_SMTP) & sim=$$!; \
	bin/221b-sim -nodes 4 -pods 40 -seed 3 -listen 127.0.0.1:7444 & sim2=$$!; \
	trap 'kill $$sim $$sim2 2>/dev/null; $(DEV_DB_STOP)' EXIT INT TERM; \
	bin/221b -api-server http://127.0.0.1:7443 -cluster-name simulated $(SIM_STORE) $(DEV_DB_FLAGS) $(SIM_SHERLOCK) $(SIM_WATSON) -log-format text $(SIM_AUTH)

# E2E_PORT is where e2e-sim serves the console, for a machine where 8080 is taken.
E2E_PORT ?= 8080
# E2E_ARGS goes to Playwright, such as --shard=2/3 or one spec file.
E2E_ARGS ?=

# A second simulator on 7444 stands in for another cluster (P3-09); the
# tests tell it which token to require.
e2e-sim: build ## Run the browser end-to-end tests against a simulated cluster and a throwaway PostgreSQL in Docker (or DEV_DATABASE)
	@$(DEV_DB_START); \
	trap '$(DEV_DB_STOP)' EXIT INT TERM; \
	bin/221b-sim -nodes $(SIM_NODES) -pods $(SIM_PODS) $(SIM_OIDC) $(SIM_SMTP) & sim=$$!; \
	bin/221b-sim -nodes 4 -pods 40 -seed 3 -listen 127.0.0.1:7444 & sim2=$$!; \
	trap 'kill $$sim $$sim2 2>/dev/null; $(DEV_DB_STOP)' EXIT INT TERM; \
	for i in $$(seq 50); do curl -fs http://127.0.0.1:7443/version >/dev/null && curl -fs http://127.0.0.1:7444/version >/dev/null && break; sleep 0.2; done; \
	bin/221b -api-server http://127.0.0.1:7443 -cluster-name simulated $(SIM_STORE) $(DEV_DB_FLAGS) $(SIM_SHERLOCK) $(SIM_WATSON) -health-interval 2s -log-format text -listen 127.0.0.1:$(E2E_PORT) $(SIM_AUTH) > bin/e2e-audit.log 2> bin/e2e-server.log & srv=$$!; \
	trap 'kill $$sim $$sim2 $$srv 2>/dev/null; $(DEV_DB_STOP)' EXIT INT TERM; \
	for i in $$(seq 600); do curl -fs http://127.0.0.1:$(E2E_PORT)/readyz >/dev/null && break; sleep 0.2; done; \
	cd web && E2E_BASE_URL=http://127.0.0.1:$(E2E_PORT) E2E_MODE=sim E2E_SIM_URL=http://127.0.0.1:7443 E2E_OTHER_SIM_URL=http://127.0.0.1:7444 $(NPM) run e2e -- $(E2E_ARGS)

screenshots: build ## Take the README's screenshots (docs/screenshots) against a simulated cluster
	@$(DEV_DB_START); \
	trap '$(DEV_DB_STOP)' EXIT INT TERM; \
	bin/221b-sim -nodes $(SIM_NODES) -pods $(SIM_PODS) $(SIM_OIDC) & sim=$$!; \
	trap 'kill $$sim 2>/dev/null; $(DEV_DB_STOP)' EXIT INT TERM; \
	for i in $$(seq 50); do curl -fs http://127.0.0.1:7443/version >/dev/null && break; sleep 0.2; done; \
	bin/221b -api-server http://127.0.0.1:7443 -cluster-name simulated $(SIM_STORE) $(DEV_DB_FLAGS) $(SIM_SHERLOCK) $(SIM_WATSON) -log-format text $(SIM_AUTH) > bin/shots-audit.log 2> bin/shots-server.log & srv=$$!; \
	trap 'kill $$sim $$srv 2>/dev/null; $(DEV_DB_STOP)' EXIT INT TERM; \
	for i in $$(seq 600); do curl -fs http://127.0.0.1:8080/readyz >/dev/null && break; sleep 0.2; done; \
	cd web && E2E_MODE=sim E2E_SIM_URL=http://127.0.0.1:7443 npx playwright test -c screenshots/playwright.config.ts

sherlock-eval: ## Ask Sherlock about the simulator's sample problems (P5-06); with ANTHROPIC_API_KEY, through the Claude API
	hack/sherlock-eval.sh

clean: ## Remove build output
	rm -rf bin web/dist
	find $(UI_DIST) -mindepth 1 ! -name .gitkeep -delete

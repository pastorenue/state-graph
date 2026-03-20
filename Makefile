.PHONY: build test test-race vet lint clean up down ui-install ui-build ui-check ui-dev

GO_IMAGE  := golang:1.22
NODE_IMAGE := node:20-alpine
DOCKER_RUN     := docker run --rm -v "$(CURDIR)":/workspace -w /workspace $(GO_IMAGE)
DOCKER_RUN_UI  := docker run --rm -v "$(CURDIR)/ui":/workspace -w /workspace $(NODE_IMAGE)

## build: compile all packages
build:
	$(DOCKER_RUN) go build ./...

## test: run unit tests (no external deps)
test:
	$(DOCKER_RUN) go test ./...

## test-race: run unit tests with race detector
test-race:
	$(DOCKER_RUN) go test -race ./...

## vet: run go vet
vet:
	$(DOCKER_RUN) go vet ./...

## lint: run go vet + staticcheck (requires staticcheck installed)
lint: vet

## up: start infrastructure (mongo, clickhouse, orchestrator)
up:
	docker compose up -d

## down: stop all services
down:
	docker compose down

## test-integration: run integration tests against live services
test-integration:
	docker compose --profile test-integration up --abort-on-container-exit --exit-code-from test-integration

## logs: tail orchestrator logs
logs:
	docker compose logs -f orchestrator

## ui-install: install UI npm dependencies
ui-install:
	$(DOCKER_RUN_UI) npm install

## ui-build: build the SvelteKit dashboard
ui-build: ui-install
	$(DOCKER_RUN_UI) npm run build

## ui-check: type-check the SvelteKit dashboard
ui-check: ui-install
	$(DOCKER_RUN_UI) npm run check

## ui-dev: run the SvelteKit dev server (host machine, not Docker)
ui-dev:
	cd ui && npm run dev

## clean: remove build artefacts
clean:
	$(DOCKER_RUN) go clean ./...
	rm -rf ui/build ui/.svelte-kit ui/node_modules

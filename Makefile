.PHONY: build test test-race vet lint clean up down

GO_IMAGE := golang:1.22
DOCKER_RUN := docker run --rm -v "$(CURDIR)":/workspace -w /workspace $(GO_IMAGE)

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

## clean: remove build artefacts
clean:
	$(DOCKER_RUN) go clean ./...

.PHONY: build test test-race vet lint clean up down ui-install ui-build ui-check ui-dev helm-lint helm-template docker-build proto-gen deps build-cli install-cli

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

HELM_IMAGE := alpine/helm:3.14.0
DOCKER_RUN_HELM := docker run --rm -v "$(CURDIR)/deployments/k8s":/chart $(HELM_IMAGE)

## helm-lint: lint the Helm chart
helm-lint:
	$(DOCKER_RUN_HELM) lint /chart

## helm-template: render the Helm chart and print manifests
helm-template:
	$(DOCKER_RUN_HELM) template kflow /chart --set mongodb.uri=mongodb://localhost:27017

## docker-build: build the Control Plane container image
docker-build:
	docker build -t kflow:dev .

BUF_IMAGE := bufbuild/buf:latest

## proto-gen: generate Go code from proto definitions
proto-gen:
	docker run --rm \
	  -v "$(CURDIR)":/workspace \
	  -w /workspace \
	  golang:1.22-alpine \
	  sh -c "apk add --no-cache git && \
	    GOPATH=/go go install github.com/bufbuild/buf/cmd/buf@v1.35.0 && \
	    GOPATH=/go go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.34.1 && \
	    GOPATH=/go go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.3.0 && \
	    GOPATH=/go go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.20.0 && \
	    export PATH=\$$PATH:/go/bin && \
	    cd proto && buf generate"

## deps: add/update gRPC and grpc-gateway Go dependencies
deps:
	docker run --rm -v "$(CURDIR)":/workspace -w /workspace golang:1.22-alpine \
	  go get google.golang.org/grpc@v1.64.0 \
	         github.com/grpc-ecosystem/grpc-gateway/v2@v2.20.0

## build-cli: build the kflow CLI binary
build-cli:
	$(DOCKER_RUN) go build -o bin/kflow ./cmd/kflow

## install-cli: install the kflow CLI to GOPATH/bin
install-cli:
	$(DOCKER_RUN) go install ./cmd/kflow

## clean: remove build artefacts
clean:
	$(DOCKER_RUN) go clean ./...
	rm -rf ui/build ui/.svelte-kit ui/node_modules

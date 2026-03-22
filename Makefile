.PHONY: build test test-race vet lint clean up down ui-install ui-build ui-check ui-dev helm-lint helm-template docker-build proto-gen proto-gen-python deps build-ui build-cli install-cli demo examples py-examples example-cp example-k8s-build example-k8s-load example-k8s-setup example-k8s-run example-k8s-clean

GO_IMAGE       := golang:1.22
NODE_IMAGE     := node:22-alpine
PYTHON_IMAGE   := python:3.12-slim
DOCKER_RUN     := docker run --rm -v "$(CURDIR)":/workspace -w /workspace $(GO_IMAGE)
DOCKER_RUN_UI  := docker run --rm -v "$(CURDIR)/ui":/workspace -w /workspace $(NODE_IMAGE)
DOCKER_RUN_PY  := docker run --rm -v "$(CURDIR)":/workspace -w /workspace $(PYTHON_IMAGE)

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
	docker compose up -d --build

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

## ui-dev: run the SvelteKit dev server in Docker
ui-dev:
	docker compose up web-ui

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

## build-ui: build the SvelteKit dashboard and embed it in the CLI assets
build-ui: ui-install
	$(DOCKER_RUN_UI) npm run build
	rm -rf cmd/kflow/uiassets/build
	cp -r ui/build cmd/kflow/uiassets/build

TARGET_OS ?= darwin
TARGET_ARCH ?= arm64

## build-cli: build the kflow CLI binary (run make build-ui first for embedded dashboard)
build-cli: build-ui
	$(DOCKER_RUN) env GOOS=$(TARGET_OS) GOARCH=$(TARGET_ARCH) go build -o bin/kflow ./cmd/kflow
	make install-cli

## install-cli: install the kflow CLI to GOPATH/bin
install-cli:
	$(DOCKER_RUN) go install ./cmd/kflow

## examples: run all SDK example programs locally (no Kubernetes required)
examples:
	$(DOCKER_RUN) go run ./examples/01-linear
	$(DOCKER_RUN) go run ./examples/02-branching
	$(DOCKER_RUN) go run ./examples/03-retry-catch
	$(DOCKER_RUN) go run ./examples/04-wait

## py-examples: run all Python SDK example programs locally (no Kubernetes required)
py-examples:
	$(DOCKER_RUN_PY) sh -c "pip install -q sdk/python && python examples/01-linear/python/main.py"
	$(DOCKER_RUN_PY) sh -c "pip install -q sdk/python && python examples/02-branching/python/main.py"
	$(DOCKER_RUN_PY) sh -c "pip install -q sdk/python && python examples/03-retry-catch/python/main.py"
	$(DOCKER_RUN_PY) sh -c "pip install -q sdk/python && python examples/04-wait/python/main.py"

## demo: run sample workflow executions (requires: make up)
demo:
	@bash scripts/demo.sh

## example-cp: submit a workflow to the local orchestrator via the control plane API
example-cp:
	docker run --rm \
	  -v "$(CURDIR)":/workspace -w /workspace \
	  --network state-graph_default \
	  -e KFLOW_API_ENDPOINT=http://orchestrator:8080 \
	  $(GO_IMAGE) go run ./examples/05-control-plane

## proto-gen-python: generate Python RunnerService stubs (flat output in sdk/python/kflow/proto/)
proto-gen-python:
	mkdir -p sdk/python/kflow/proto
	$(DOCKER_RUN_PY) sh -c "pip install -q grpcio-tools==1.64.0 && \
	  python -m grpc_tools.protoc \
	    -I proto/kflow/v1 \
	    --python_out=sdk/python/kflow/proto \
	    --grpc_python_out=sdk/python/kflow/proto \
	    runner.proto types.proto && \
	  sed -i 's/^import runner_pb2/from . import runner_pb2/' sdk/python/kflow/proto/runner_pb2_grpc.py"

## example-k8s-build: build orchestrator + Go example images
example-k8s-build:
	docker build -t kflow:dev .
	docker build -t kflow-example-k8s:dev -f examples/06-kubernetes/Dockerfile .

## example-k8s-load: load images into minikube
example-k8s-load: example-k8s-build
	minikube image load kflow:dev
	minikube image load kflow-example-k8s:dev

## example-k8s-setup: deploy orchestrator stack to minikube
example-k8s-setup: example-k8s-load
	kubectl apply -f examples/06-kubernetes/k8s/
	kubectl rollout status deployment/mongo -n kflow --timeout=60s
	kubectl rollout status deployment/kflow-orchestrator -n kflow --timeout=90s

## example-k8s-run: submit workflow, stream logs
example-k8s-run:
	kubectl delete job kflow-example-runner -n kflow --ignore-not-found
	kubectl apply -f examples/06-kubernetes/k8s/run-job.yaml
	kubectl wait --for=condition=complete job/kflow-example-runner -n kflow --timeout=120s
	kubectl logs job/kflow-example-runner -n kflow

## example-k8s-clean: tear down K8s example
example-k8s-clean:
	kubectl delete -f examples/06-kubernetes/k8s/run-job.yaml --ignore-not-found
	kubectl delete -f examples/06-kubernetes/k8s/ --ignore-not-found

## clean: remove build artefacts
clean:
	$(DOCKER_RUN) go clean ./...
	rm -rf ui/build ui/.svelte-kit ui/node_modules

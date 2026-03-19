# Phase 10 — Helm Chart & Deployment

## Goal

Package the entire system for production Kubernetes deployment using a Helm chart in `deployments/k8s/`. The chart deploys the Control Plane, and optionally configures external MongoDB and ClickHouse connections. It also resolves the open dashboard deployment question (embedded vs. separate container) and documents the full operational runbook for a production cluster.

---

## Phase Dependencies

All prior phases must be complete and stable:
- **Phase 4**: `cmd/orchestrator` binary — the Control Plane container image.
- **Phase 5**: HTTP API and Service reconciliation are live.
- **Phase 6**: Telemetry and ClickHouse integration are live.
- **Phase 9**: Dashboard `ui/build/` artifact exists. The deployment method (embedded vs. separate) is decided here.

---

## Files to Create

| File | Purpose |
|------|---------|
| `deployments/k8s/Chart.yaml` | Helm chart metadata |
| `deployments/k8s/values.yaml` | Default values for all configurable parameters |
| `deployments/k8s/templates/deployment.yaml` | Control Plane `Deployment` |
| `deployments/k8s/templates/service.yaml` | Control Plane `Service` (ClusterIP) |
| `deployments/k8s/templates/ingress.yaml` | Optional Ingress for external Control Plane access |
| `deployments/k8s/templates/serviceaccount.yaml` | `ServiceAccount` for the Control Plane pod |
| `deployments/k8s/templates/rbac.yaml` | `ClusterRole` + `ClusterRoleBinding` for K8s API access |
| `deployments/k8s/templates/secret.yaml` | Secret holding `KFLOW_MONGO_URI` and `KFLOW_CLICKHOUSE_DSN` |
| `deployments/k8s/templates/configmap.yaml` | Non-secret config: namespace, MongoDB DB name, log level |
| `deployments/k8s/templates/_helpers.tpl` | Shared template helpers: name, labels, selector labels |

---

## Chart Metadata (`Chart.yaml`)

```yaml
apiVersion: v2
name: kflow
description: Kubernetes-native serverless workflow engine — Control Plane
type: application
version: 0.1.0         # Chart version; bumped independently of appVersion
appVersion: "0.1.0"    # Control Plane binary version
```

---

## Values (`values.yaml`)

All user-facing configuration lives here. Templates reference `{{ .Values.* }}` exclusively.

```yaml
# Control Plane image
image:
  repository: ghcr.io/your-org/kflow
  tag: ""          # defaults to Chart.appVersion if empty
  pullPolicy: IfNotPresent

# Number of Control Plane replicas (1 for v1; HA not yet supported)
replicaCount: 1

# Kubernetes namespace where kflow creates Jobs and Deployments for user workloads.
# This is also the namespace where the Control Plane itself runs.
workloadNamespace: kflow

# Control Plane HTTP server port
service:
  port: 8080
  type: ClusterIP

# External Ingress for the Control Plane API + Dashboard
ingress:
  enabled: false
  className: nginx
  host: ""           # e.g. kflow.example.com
  tls: []            # standard Helm ingress TLS block

# MongoDB connection (external; chart does not deploy MongoDB)
mongodb:
  uri: ""            # required; set via --set mongodb.uri=... or existingSecret
  database: kflow
  existingSecret: "" # if set, use this Secret for KFLOW_MONGO_URI instead of creating one

# ClickHouse connection (external; chart does not deploy ClickHouse)
clickhouse:
  dsn: ""            # optional; if empty, telemetry is disabled
  existingSecret: "" # if set, use this Secret for KFLOW_CLICKHOUSE_DSN

# Dashboard deployment method (resolves Phase 9 open question)
# "embedded" — served by the Control Plane binary (go:embed ui/build)
# "sidecar"  — separate nginx container in the Control Plane Pod
dashboard:
  mode: embedded     # "embedded" | "sidecar"
  sidecar:
    image:
      repository: nginx
      tag: alpine
      pullPolicy: IfNotPresent
    port: 80

# Resource requests and limits for the Control Plane container
resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 500m
    memory: 512Mi

# Pod-level settings
podAnnotations: {}
podSecurityContext: {}
securityContext: {}
nodeSelector: {}
tolerations: []
affinity: {}

# ServiceAccount
serviceAccount:
  create: true
  name: ""           # defaults to chart fullname if empty
  annotations: {}
```

---

## RBAC Requirements

The Control Plane requires the following Kubernetes API permissions. These are created as a `ClusterRole` (because it manages Jobs, Deployments, and Services across namespaces, or within the `workloadNamespace`).

```yaml
rules:
  # Job management (Task execution + Lambda services)
  - apiGroups: ["batch"]
    resources: ["jobs"]
    verbs: ["create", "get", "list", "watch", "delete"]

  # Pod log streaming (StreamJobLogs)
  - apiGroups: [""]
    resources: ["pods", "pods/log"]
    verbs: ["get", "list", "watch"]

  # Deployment management (Deployment-mode services)
  - apiGroups: ["apps"]
    resources: ["deployments"]
    verbs: ["create", "get", "list", "watch", "update", "patch", "delete"]

  # K8s Service management (Deployment-mode service routing)
  - apiGroups: [""]
    resources: ["services"]
    verbs: ["create", "get", "list", "watch", "delete"]

  # Ingress management (Expose())
  - apiGroups: ["networking.k8s.io"]
    resources: ["ingresses"]
    verbs: ["create", "get", "list", "watch", "delete"]
```

The `ClusterRoleBinding` binds this role to the chart's `ServiceAccount`. If `values.yaml` scopes workloads to a single namespace, a `Role`/`RoleBinding` pair can be used instead — parameterise with `rbac.clusterScoped: true` (default).

---

## Dashboard Deployment

This chart resolves the open question from Phase 9:

### Option A: `dashboard.mode: embedded` (default)

The Control Plane binary embeds `ui/build/` via `go:embed` in `cmd/orchestrator/main.go`. The single container serves both the API and the static SPA. No extra K8s resources are required.

```go
//go:embed ui/build
var dashboardFS embed.FS
// Served at / (or /ui/ if paths.base was set in svelte.config.js)
```

### Option B: `dashboard.mode: sidecar`

An nginx sidecar container is added to the Control Plane Pod. It serves the pre-built `ui/build/` assets from a shared `emptyDir` volume populated by an init container.

```yaml
# In templates/deployment.yaml when dashboard.mode == "sidecar":
initContainers:
  - name: dashboard-copy
    image: ghcr.io/your-org/kflow-ui:{{ .Values.image.tag }}
    command: ["cp", "-r", "/app/build/.", "/dashboard/"]
    volumeMounts:
      - name: dashboard
        mountPath: /dashboard

containers:
  - name: nginx
    image: nginx:alpine
    ports:
      - containerPort: 80
    volumeMounts:
      - name: dashboard
        mountPath: /usr/share/nginx/html

volumes:
  - name: dashboard
    emptyDir: {}
```

The nginx container requires a configmap-mounted `nginx.conf` that proxies `/api/` to the Control Plane container (`localhost:8080`).

**Default**: `embedded`. Switch to `sidecar` when independent UI deployments are required.

---

## Secret Management

When `mongodb.existingSecret` is empty, the chart creates a `Secret` from `mongodb.uri`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: {{ include "kflow.fullname" . }}-secrets
stringData:
  KFLOW_MONGO_URI: {{ .Values.mongodb.uri | quote }}
  KFLOW_CLICKHOUSE_DSN: {{ .Values.clickhouse.dsn | quote }}
```

The `Deployment` mounts these as env vars via `envFrom.secretRef`. When `existingSecret` is set, the chart references that secret instead of creating one.

**Never commit `mongodb.uri` or `clickhouse.dsn` values to source control.** Use `--set` flags, a `values.override.yaml` (gitignored), or a secrets manager (Vault, External Secrets Operator) in production.

---

## Container Image Build

The chart does not build images. Images must be built and pushed before `helm install`. The recommended build process:

```dockerfile
# Dockerfile at repo root
FROM golang:1.23-alpine AS build-go
WORKDIR /app
COPY go.* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o orchestrator ./cmd/orchestrator

FROM node:22-alpine AS build-ui
WORKDIR /ui
COPY ui/package*.json ./
RUN npm ci
COPY ui/ .
RUN npm run build

FROM gcr.io/distroless/static:nonroot
COPY --from=build-go /app/orchestrator /orchestrator
# For embedded mode: copy ui/build into the binary's embed path
COPY --from=build-ui /ui/build /ui/build
ENTRYPOINT ["/orchestrator"]
```

The single multi-stage Dockerfile produces one image for both embedded and sidecar modes. In sidecar mode, the nginx container uses only the `/ui/build` layer (or a separate `kflow-ui` image can be built from the `build-ui` stage).

---

## Operational Notes

### Namespace Strategy

The Control Plane runs in the `workloadNamespace` (default: `kflow`). All user workloads (Jobs, Deployments, K8s Services, Ingresses) are also created in this namespace. Single-namespace operation simplifies RBAC and resource visibility.

Multi-namespace isolation (one namespace per workflow or per user) is a future feature.

### MongoDB Requirements

- The chart does not deploy MongoDB. Use an external MongoDB service (Atlas, self-hosted, or a separate Helm chart such as `bitnami/mongodb`).
- Minimum MongoDB version: 5.0 (required for `$set` pipeline in update operations).
- The Control Plane connects with a single URI; replica set URIs are supported and recommended for production.

### ClickHouse Requirements

- The chart does not deploy ClickHouse. Use an external ClickHouse service (ClickHouse Cloud, `clickhouse/clickhouse` Helm chart, or Altinity operator).
- Minimum ClickHouse version: 23.x (for `DateTime64(3)` and `generateUUIDv4()` support).
- If `clickhouse.dsn` is empty, the Control Plane starts with telemetry disabled — no error, no crash.

### Health Checks

The Control Plane exposes two endpoints used by Kubernetes probes:

| Endpoint | Probe type | Success condition |
|----------|------------|-------------------|
| `GET /healthz` | Liveness | Returns `200 OK` always (process alive) |
| `GET /readyz` | Readiness | Returns `200 OK` only after MongoDB connection and `EnsureIndexes` succeed |

These endpoints are defined in Phase 5 (`internal/api/server.go`) but are documented here because the Helm chart depends on them for rollout safety.

### Upgrade Strategy

The `Deployment` uses `RollingUpdate` with:
```yaml
strategy:
  type: RollingUpdate
  rollingUpdate:
    maxUnavailable: 0
    maxSurge: 1
```

This ensures zero-downtime upgrades. Since `replicaCount: 1` is the default, there is momentary interruption during upgrades until HA is implemented.

---

## Design Invariants

1. The chart never deploys MongoDB or ClickHouse. These are always external dependencies.
2. Secret values (`mongodb.uri`, `clickhouse.dsn`) are never stored in `values.yaml` committed to git. They are always passed via `--set`, override files, or `existingSecret` references.
3. `dashboard.mode: embedded` is the default. `sidecar` is opt-in for teams that need independent UI deployments.
4. The RBAC `ClusterRole` grants minimum required permissions. No `*` wildcard verbs or resources.
5. The chart must pass `helm lint` with zero errors and zero warnings.
6. The chart must pass `helm template | kubectl --dry-run=client apply -f -` without errors.
7. All template labels follow the standard `app.kubernetes.io/*` conventions via `_helpers.tpl`.
8. The liveness probe never checks MongoDB. The readiness probe does. This prevents the pod from being killed during a transient MongoDB timeout.
9. `values.yaml` is the single source of truth for all configurable parameters. No hardcoded values in templates.
10. The `workloadNamespace` value is propagated to the Control Plane as `KFLOW_NAMESPACE` env var so the K8s client creates resources in the correct namespace.

---

## Acceptance Criteria / Verification

- [ ] `helm lint deployments/k8s/` passes with zero errors and zero warnings.
- [ ] `helm template kflow deployments/k8s/ --set mongodb.uri=mongodb://localhost:27017` renders valid YAML.
- [ ] Rendered YAML passes `kubectl --dry-run=client apply -f -` against a live cluster.
- [ ] `helm install kflow deployments/k8s/ --set mongodb.uri=<uri>` deploys successfully.
- [ ] Control Plane pod reaches `Running` and passes readiness probe within 60 seconds.
- [ ] `GET /healthz` returns `200 OK` immediately.
- [ ] `GET /readyz` returns `200 OK` only after MongoDB index creation completes.
- [ ] With `ingress.enabled=true` and a valid `ingress.host`, the Ingress is created and the API is reachable at that host.
- [ ] With `dashboard.mode=embedded`, the SvelteKit SPA loads at `http://<host>/`.
- [ ] With `dashboard.mode=sidecar`, the nginx container serves the SPA and proxies `/api/` correctly.
- [ ] With `clickhouse.dsn` empty, the Control Plane starts with no errors and telemetry endpoints return `{"events":[]}` / `{"metrics":[]}` / `{"logs":[],"total":0}`.
- [ ] `helm upgrade kflow deployments/k8s/ --set image.tag=<new>` completes with zero-downtime rolling update.
- [ ] `helm uninstall kflow` removes all chart-created resources. User workload Jobs and Deployments (created at runtime) are not removed by uninstall — document this in operational notes.

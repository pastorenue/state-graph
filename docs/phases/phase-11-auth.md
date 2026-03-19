# Phase 11 — Auth Model

## Goal

Add authentication to the Control Plane API and the SvelteKit dashboard. All API routes (except health probes) require a Bearer token. The dashboard authenticates via a login page and stores a signed session token. v1 is intentionally minimal: single shared API key, no RBAC, no token rotation, no OAuth/OIDC.

---

## Phase Dependencies

- **Phase 5** must be complete. HTTP routes and `internal/api/server.go` must be stable.
- **Phase 9** must be complete. The SvelteKit dashboard login page is part of this phase.

---

## Files to Create

| File | Purpose |
|------|---------|
| `internal/api/auth_middleware.go` | Bearer token validation middleware for all API routes |
| `internal/api/auth_handler.go` | `POST /api/v1/auth/token` — validates API key, issues signed session token |

---

## Configuration

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `KFLOW_API_KEY` | No | `""` | Shared API key for all Control Plane access. If empty, auth middleware is **disabled** (dev mode only — never deploy without a key). |

`KFLOW_API_KEY` is added to `internal/config/config.go`:

```go
// APIKey is the shared secret for Control Plane authentication.
// Source: KFLOW_API_KEY
// If empty, authentication is disabled (development mode).
APIKey string
```

`LoadConfig()` does not error when `KFLOW_API_KEY` is empty — the middleware self-disables. A startup warning must be logged when auth is disabled.

---

## Control Plane API Authentication

### Bearer Token

All routes under `/api/v1` (including `/api/v1/ws`) require an `Authorization` header:

```
Authorization: Bearer <token>
```

Where `<token>` is either:
- The raw `KFLOW_API_KEY` value (for direct CLI / programmatic access), or
- A signed session token issued by `POST /api/v1/auth/token` (for dashboard use).

### Auth Middleware — `internal/api/auth_middleware.go`

```go
// BearerAuthMiddleware returns an http.Handler middleware that validates
// Bearer tokens on all requests.
//
// Exempt routes (no token required):
//   - GET /healthz
//   - GET /readyz
//
// Token validation order:
//   1. Check Authorization header for "Bearer <token>".
//   2. If absent, check ?token=<value> query param (WebSocket upgrade only).
//   3. Validate token (see validation rules below).
//   4. On failure: respond 401 Unauthorized with {"error": "unauthorized", "code": "auth_required"}.
func BearerAuthMiddleware(apiKey string) func(http.Handler) http.Handler
```

Token validation rules:
1. If `apiKey == ""` (dev mode): all tokens are accepted; middleware is a no-op pass-through.
2. If the token equals `apiKey` directly: accepted (raw key access).
3. If the token is a valid HMAC-SHA256 signed session token (see below): accepted.
4. Otherwise: rejected with `401`.

### WebSocket Auth

WebSocket upgrade requests cannot easily set custom headers from browsers. The WebSocket endpoint (`GET /api/v1/ws`) additionally accepts the token as a query parameter:

```
GET /api/v1/ws?token=<token>
```

The middleware checks `?token=` only when the `Authorization` header is absent. The `?token=` parameter is accepted **only** on the `/api/v1/ws` path. All other routes must use the `Authorization` header.

---

## Session Token — `POST /api/v1/auth/token`

Defined in `internal/api/auth_handler.go`.

### Request

```
POST /api/v1/auth/token
Content-Type: application/json

{ "api_key": "<KFLOW_API_KEY value>" }
```

### Validation

1. Compare `request.api_key` against `config.APIKey` using `crypto/subtle.ConstantTimeCompare` (timing-safe).
2. If mismatch: return `401 Unauthorized` with `{"error": "invalid api key", "code": "auth_invalid"}`.

### Response (200 OK)

```json
{ "token": "<signed session token>" }
```

### Session Token Format

The session token is an HMAC-SHA256 signed payload. Structure:

```
base64url(<json_payload>) + "." + base64url(hmac_sha256(<json_payload>, KFLOW_API_KEY))
```

Where `<json_payload>` is:

```json
{ "issued_at": "<ISO 8601 timestamp>", "expires_at": "<ISO 8601 timestamp>" }
```

Token lifetime: **24 hours** from issuance. The middleware must reject expired tokens.

Signing key: `KFLOW_API_KEY` (the raw string, used as the HMAC key).

> **v1 constraint:** No token rotation. Changing `KFLOW_API_KEY` invalidates all issued tokens. Users must re-login.

---

## Dashboard Authentication — SvelteKit

### Login Page — `ui/src/routes/login/+page.svelte`

- Simple form: single "API Key" input field, "Login" button.
- On submit: `POST /api/v1/auth/token` with `{ "api_key": "<value>" }`.
- On success: store the returned `token` in `localStorage` under the key `"kflow_token"`.
- On success: redirect to `/` (executions overview).
- On failure: show inline error message.

### Auth Guard

All dashboard routes (except `/login`) check for `localStorage["kflow_token"]` on mount:
- If absent: redirect to `/login`.
- If present and expired (client-side expiry check on the `expires_at` field): redirect to `/login` and clear `localStorage`.

### API Calls with Auth

`ui/src/lib/api.ts` reads `localStorage["kflow_token"]` and includes it in every request:

```typescript
headers: {
  'Authorization': `Bearer ${token}`,
  'Content-Type': 'application/json',
}
```

If any API call returns `401`, the client clears `localStorage["kflow_token"]` and redirects to `/login`.

### WebSocket Auth

`ui/src/lib/ws.ts` reads the token from `localStorage` and appends it as a query parameter on connect:

```typescript
const ws = new WebSocket(`${wsBase}/api/v1/ws?token=${token}`);
```

If the WebSocket upgrade returns `401`, the client redirects to `/login`.

---

## v1 Constraints

The following are explicitly **out of scope** for v1 and must not be implemented:

- Token rotation or refresh tokens
- RBAC or per-user permissions
- Multi-tenant isolation
- OAuth 2.0 / OIDC / SSO integration
- Per-route permission levels
- Token revocation lists

These constraints are documented here to prevent scope creep. Future versions may address them without breaking the v1 contract.

---

## Design Invariants

1. `/healthz` and `/readyz` are **always exempt** from auth middleware. Kubernetes probes must reach them without credentials.
2. Auth middleware is a **no-op** when `KFLOW_API_KEY` is empty. This is the only supported dev-mode bypass; no other bypass mechanism exists.
3. Token comparison **must** use `crypto/subtle.ConstantTimeCompare` to prevent timing attacks on the API key.
4. Session tokens are signed with HMAC-SHA256 using `KFLOW_API_KEY` as the key. The middleware must verify the signature before accepting a session token.
5. Expired session tokens are rejected with `401`, not silently accepted.
6. The `?token=` query parameter is accepted **only** on the `/api/v1/ws` WebSocket upgrade path. All other routes require the `Authorization` header.
7. The dashboard **never** stores the raw `KFLOW_API_KEY` in `localStorage` — only the signed session token returned by `POST /api/v1/auth/token`.

---

## Acceptance Criteria / Verification

- [ ] `go build ./internal/api/...` compiles with zero errors after adding auth files.
- [ ] `GET /healthz` returns `200` without any `Authorization` header.
- [ ] `GET /readyz` returns `200` (or `503`) without any `Authorization` header.
- [ ] `GET /api/v1/executions` without a token returns `401`.
- [ ] `GET /api/v1/executions` with `Authorization: Bearer <KFLOW_API_KEY>` returns `200`.
- [ ] `POST /api/v1/auth/token` with correct key returns `200` and a token.
- [ ] `POST /api/v1/auth/token` with wrong key returns `401`.
- [ ] Signed session token from `POST /api/v1/auth/token` is accepted on `GET /api/v1/executions`.
- [ ] Expired session token (manually crafted with past `expires_at`) is rejected with `401`.
- [ ] `GET /api/v1/ws?token=<valid>` upgrades successfully.
- [ ] `GET /api/v1/ws` without token returns `401`.
- [ ] When `KFLOW_API_KEY` is unset: all routes (including `/api/v1/executions`) return `200` without a token (dev mode).
- [ ] A startup warning is logged when `KFLOW_API_KEY` is empty.
- [ ] Dashboard: `/login` page is accessible without a token; all other routes redirect to `/login` when no token in `localStorage`.
- [ ] Dashboard: successful login stores a token in `localStorage` and redirects to `/`.
- [ ] Dashboard: `401` response from any API call clears `localStorage` and redirects to `/login`.

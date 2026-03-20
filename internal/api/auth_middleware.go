package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// exemptPaths are always allowed without a token.
var exemptPaths = map[string]bool{
	"/healthz":              true,
	"/readyz":               true,
	"/api/v1/auth/status":   true,
}

// tokenPayload is the JSON body of a session token.
type tokenPayload struct {
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// BearerAuthMiddleware returns an http.Handler middleware that enforces bearer
// token authentication. When apiKey is empty the middleware is a no-op (dev mode).
func BearerAuthMiddleware(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if apiKey == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Always exempt health probes and login endpoint.
			if exemptPaths[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			if r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/token" {
				next.ServeHTTP(w, r)
				return
			}

			token := r.Header.Get("Authorization")
			if strings.HasPrefix(token, "Bearer ") {
				token = strings.TrimPrefix(token, "Bearer ")
			} else if r.URL.Path == "/api/v1/ws" {
				// WebSocket: fall back to ?token= query param.
				token = r.URL.Query().Get("token")
			} else {
				token = ""
			}

			if token == "" {
				writeError(w, http.StatusUnauthorized, "unauthorized", "auth_required")
				return
			}

			if validateToken(token, apiKey) {
				next.ServeHTTP(w, r)
				return
			}

			writeError(w, http.StatusUnauthorized, "unauthorized", "auth_required")
		})
	}
}

// validateToken returns true if the token is the raw API key or a valid
// HMAC-SHA256 session token signed with apiKey.
func validateToken(token, apiKey string) bool {
	// Raw API key comparison.
	if subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) == 1 {
		return true
	}

	// Session token: <base64url(payload)>.<base64url(sig)>
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}

	expectedSig := signPayload(payloadBytes, apiKey)
	actualSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}

	if subtle.ConstantTimeCompare(expectedSig, actualSig) != 1 {
		return false
	}

	var p tokenPayload
	if err := json.Unmarshal(payloadBytes, &p); err != nil {
		return false
	}

	return time.Now().Before(p.ExpiresAt)
}

// signPayload returns the HMAC-SHA256 signature of payload using apiKey.
func signPayload(payload []byte, apiKey string) []byte {
	mac := hmac.New(sha256.New, []byte(apiKey))
	mac.Write(payload)
	return mac.Sum(nil)
}

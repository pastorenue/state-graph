package api

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"
)

func (s *Server) handleAuthStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"auth_enabled": s.APIKey != ""})
}

func (s *Server) handleAuthToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "bad_request")
		return
	}

	if subtle.ConstantTimeCompare([]byte(req.APIKey), []byte(s.APIKey)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid api key", "auth_invalid")
		return
	}

	token, err := issueSessionToken(s.APIKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue token", "internal")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

// issueSessionToken builds and signs a 24-hour session token.
func issueSessionToken(apiKey string) (string, error) {
	now := time.Now().UTC()
	p := tokenPayload{
		IssuedAt:  now,
		ExpiresAt: now.Add(24 * time.Hour),
	}

	payloadBytes, err := json.Marshal(p)
	if err != nil {
		return "", err
	}

	payloadEnc := base64.RawURLEncoding.EncodeToString(payloadBytes)
	sig := signPayload(payloadBytes, apiKey)
	sigEnc := base64.RawURLEncoding.EncodeToString(sig)

	return payloadEnc + "." + sigEnc, nil
}

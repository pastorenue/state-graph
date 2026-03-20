// Package runner implements state token generation and validation for K8s Job
// containers. Tokens are HMAC-SHA256 signed and carry (execID, stateName,
// attempt, expiry) so the RunnerServiceServer can authorise callbacks.
package runner

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const tokenTTL = 24 * time.Hour

var (
	ErrInvalidToken = errors.New("runner: invalid state token")
	ErrTokenExpired = errors.New("runner: state token has expired")
)

type tokenClaims struct {
	ExecID    string `json:"eid"`
	StateName string `json:"sn"`
	Attempt   int    `json:"att"`
	ExpiresAt int64  `json:"exp"`
}

// GenerateStateToken creates an HMAC-SHA256 signed token authorising a single
// (execID, stateName, attempt) execution. The token is valid for 24 hours.
func GenerateStateToken(execID, stateName string, attempt int, secret []byte) (string, error) {
	claims := tokenClaims{
		ExecID:    execID,
		StateName: stateName,
		Attempt:   attempt,
		ExpiresAt: time.Now().Add(tokenTTL).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("runner: marshal token claims: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	sig := computeHMAC(secret, encoded)
	return encoded + "." + sig, nil
}

// ValidateStateToken parses and verifies a state token. Returns the embedded
// claims on success. Uses constant-time comparison to prevent timing attacks.
func ValidateStateToken(token string, secret []byte) (execID, stateName string, attempt int, err error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", "", 0, ErrInvalidToken
	}
	encoded, sig := parts[0], parts[1]

	expected := computeHMAC(secret, encoded)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return "", "", 0, ErrInvalidToken
	}

	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", 0, ErrInvalidToken
	}

	var claims tokenClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return "", "", 0, ErrInvalidToken
	}

	if time.Now().Unix() > claims.ExpiresAt {
		return "", "", 0, ErrTokenExpired
	}

	return claims.ExecID, claims.StateName, claims.Attempt, nil
}

func computeHMAC(secret []byte, data string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

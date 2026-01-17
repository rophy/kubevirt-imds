package imds

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// TokenResponse is the response for GET /v1/token
type TokenResponse struct {
	Token               string    `json:"token"`
	ExpirationTimestamp time.Time `json:"expirationTimestamp,omitempty"`
}

// IdentityResponse is the response for GET /v1/identity
type IdentityResponse struct {
	Namespace          string `json:"namespace"`
	ServiceAccountName string `json:"serviceAccountName"`
	VMName             string `json:"vmName"`
	PodName            string `json:"podName"`
}

// ErrorResponse is the response for errors
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// handleHealthz handles GET /healthz
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// handleToken handles GET /v1/token
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read token from file
	tokenBytes, err := os.ReadFile(s.TokenPath)
	if err != nil {
		log.Printf("Failed to read token from %s: %v", s.TokenPath, err)
		s.writeError(w, http.StatusInternalServerError, "token_unavailable", "Failed to read ServiceAccount token")
		return
	}

	token := strings.TrimSpace(string(tokenBytes))
	resp := TokenResponse{
		Token: token,
	}

	// Parse JWT to extract expiration time
	if exp, err := parseJWTExpiration(token); err == nil {
		resp.ExpirationTimestamp = exp
	}

	s.writeJSON(w, http.StatusOK, resp)
}

// handleIdentity handles GET /v1/identity
func (s *Server) handleIdentity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	resp := IdentityResponse{
		Namespace:          s.Namespace,
		ServiceAccountName: s.ServiceAccountName,
		VMName:             s.VMName,
		PodName:            s.PodName,
	}

	s.writeJSON(w, http.StatusOK, resp)
}

// writeJSON writes a JSON response
func (s *Server) writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("Failed to encode JSON response: %v", err)
	}
}

// writeError writes an error response
func (s *Server) writeError(w http.ResponseWriter, status int, errCode, message string) {
	resp := ErrorResponse{
		Error:   errCode,
		Message: message,
	}
	s.writeJSON(w, status, resp)
}

// parseJWTExpiration extracts the expiration time from a JWT token.
// JWTs have three base64-encoded parts separated by dots: header.payload.signature
// The payload contains the "exp" claim as a Unix timestamp.
func parseJWTExpiration(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("invalid JWT format")
	}

	// Decode the payload (second part)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	// Parse the JSON payload
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("failed to parse JWT claims: %w", err)
	}

	if claims.Exp == 0 {
		return time.Time{}, fmt.Errorf("no exp claim in token")
	}

	return time.Unix(claims.Exp, 0), nil
}

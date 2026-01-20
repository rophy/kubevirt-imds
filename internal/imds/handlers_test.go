package imds

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestParseJWTExpiration(t *testing.T) {
	tests := []struct {
		name      string
		token     string
		wantExp   time.Time
		wantError bool
	}{
		{
			name:      "valid token with exp claim",
			token:     createTestJWT(t, map[string]interface{}{"exp": 1700000000}),
			wantExp:   time.Unix(1700000000, 0),
			wantError: false,
		},
		{
			name:      "valid token with exp in future",
			token:     createTestJWT(t, map[string]interface{}{"exp": 1800000000, "iat": 1700000000}),
			wantExp:   time.Unix(1800000000, 0),
			wantError: false,
		},
		{
			name:      "token without exp claim",
			token:     createTestJWT(t, map[string]interface{}{"iat": 1700000000}),
			wantExp:   time.Time{},
			wantError: true,
		},
		{
			name:      "token with exp=0",
			token:     createTestJWT(t, map[string]interface{}{"exp": 0}),
			wantExp:   time.Time{},
			wantError: true,
		},
		{
			name:      "invalid JWT format - no dots",
			token:     "notavalidtoken",
			wantExp:   time.Time{},
			wantError: true,
		},
		{
			name:      "invalid JWT format - only one part",
			token:     "header.payload",
			wantExp:   time.Time{},
			wantError: true,
		},
		{
			name:      "invalid JWT format - too many parts",
			token:     "a.b.c.d",
			wantExp:   time.Time{},
			wantError: true,
		},
		{
			name:      "invalid base64 in payload",
			token:     "header.!!!invalid!!!.signature",
			wantExp:   time.Time{},
			wantError: true,
		},
		{
			name:      "invalid JSON in payload",
			token:     "header." + base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".signature",
			wantExp:   time.Time{},
			wantError: true,
		},
		{
			name:      "empty token",
			token:     "",
			wantExp:   time.Time{},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseJWTExpiration(tt.token)
			if tt.wantError {
				if err == nil {
					t.Errorf("parseJWTExpiration() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("parseJWTExpiration() unexpected error: %v", err)
				return
			}
			if !got.Equal(tt.wantExp) {
				t.Errorf("parseJWTExpiration() = %v, want %v", got, tt.wantExp)
			}
		})
	}
}

func TestHandleHealthz(t *testing.T) {
	server := &Server{}

	tests := []struct {
		name       string
		method     string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "GET request returns OK",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantBody:   "OK",
		},
		{
			name:       "POST request returns method not allowed",
			method:     http.MethodPost,
			wantStatus: http.StatusMethodNotAllowed,
			wantBody:   "",
		},
		{
			name:       "PUT request returns method not allowed",
			method:     http.MethodPut,
			wantStatus: http.StatusMethodNotAllowed,
			wantBody:   "",
		},
		{
			name:       "DELETE request returns method not allowed",
			method:     http.MethodDelete,
			wantStatus: http.StatusMethodNotAllowed,
			wantBody:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/healthz", nil)
			w := httptest.NewRecorder()

			server.handleHealthz(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("handleHealthz() status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && w.Body.String() != tt.wantBody {
				t.Errorf("handleHealthz() body = %q, want %q", w.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestHandleToken(t *testing.T) {
	tests := []struct {
		name         string
		method       string
		tokenContent string
		tokenExists  bool
		wantStatus   int
		checkBody    func(t *testing.T, body string)
	}{
		{
			name:         "GET request returns token",
			method:       http.MethodGet,
			tokenContent: createTestJWT(t, map[string]interface{}{"exp": 1700000000}),
			tokenExists:  true,
			wantStatus:   http.StatusOK,
			checkBody: func(t *testing.T, body string) {
				var resp TokenResponse
				if err := json.Unmarshal([]byte(body), &resp); err != nil {
					t.Errorf("failed to parse response: %v", err)
					return
				}
				if resp.Token == "" {
					t.Error("expected token in response")
				}
				if resp.ExpirationTimestamp.IsZero() {
					t.Error("expected expirationTimestamp in response")
				}
			},
		},
		{
			name:         "GET request returns token without exp",
			method:       http.MethodGet,
			tokenContent: createTestJWT(t, map[string]interface{}{"iat": 1700000000}),
			tokenExists:  true,
			wantStatus:   http.StatusOK,
			checkBody: func(t *testing.T, body string) {
				var resp TokenResponse
				if err := json.Unmarshal([]byte(body), &resp); err != nil {
					t.Errorf("failed to parse response: %v", err)
					return
				}
				if resp.Token == "" {
					t.Error("expected token in response")
				}
				// expirationTimestamp should be omitted (zero value)
			},
		},
		{
			name:         "GET request with invalid token content",
			method:       http.MethodGet,
			tokenContent: "not-a-valid-jwt",
			tokenExists:  true,
			wantStatus:   http.StatusOK,
			checkBody: func(t *testing.T, body string) {
				var resp TokenResponse
				if err := json.Unmarshal([]byte(body), &resp); err != nil {
					t.Errorf("failed to parse response: %v", err)
					return
				}
				// Invalid token is still returned (server doesn't validate JWT)
				if resp.Token != "not-a-valid-jwt" {
					t.Errorf("expected raw token content, got %q", resp.Token)
				}
				// No expiration since token is invalid
				if !resp.ExpirationTimestamp.IsZero() {
					t.Error("expected no expirationTimestamp for invalid token")
				}
			},
		},
		{
			name:        "GET request with missing token file",
			method:      http.MethodGet,
			tokenExists: false,
			wantStatus:  http.StatusInternalServerError,
			checkBody: func(t *testing.T, body string) {
				var resp ErrorResponse
				if err := json.Unmarshal([]byte(body), &resp); err != nil {
					t.Errorf("failed to parse error response: %v", err)
					return
				}
				if resp.Error != "token_unavailable" {
					t.Errorf("expected error code 'token_unavailable', got %q", resp.Error)
				}
			},
		},
		{
			name:        "POST request returns method not allowed",
			method:      http.MethodPost,
			tokenExists: true,
			wantStatus:  http.StatusMethodNotAllowed,
			checkBody:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp directory for token file
			tmpDir := t.TempDir()
			tokenPath := filepath.Join(tmpDir, "token")

			if tt.tokenExists {
				if err := os.WriteFile(tokenPath, []byte(tt.tokenContent), 0644); err != nil {
					t.Fatalf("failed to write token file: %v", err)
				}
			}

			server := &Server{TokenPath: tokenPath}

			req := httptest.NewRequest(tt.method, "/v1/token", nil)
			w := httptest.NewRecorder()

			server.handleToken(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("handleToken() status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.checkBody != nil {
				tt.checkBody(t, w.Body.String())
			}
		})
	}
}

func TestHandleIdentity(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		server     *Server
		wantStatus int
		checkBody  func(t *testing.T, body string)
	}{
		{
			name:   "GET request returns identity",
			method: http.MethodGet,
			server: &Server{
				Namespace:          "test-namespace",
				ServiceAccountName: "test-sa",
				VMName:             "test-vm",
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, body string) {
				var resp IdentityResponse
				if err := json.Unmarshal([]byte(body), &resp); err != nil {
					t.Errorf("failed to parse response: %v", err)
					return
				}
				if resp.Namespace != "test-namespace" {
					t.Errorf("namespace = %q, want %q", resp.Namespace, "test-namespace")
				}
				if resp.ServiceAccountName != "test-sa" {
					t.Errorf("serviceAccountName = %q, want %q", resp.ServiceAccountName, "test-sa")
				}
				if resp.VMName != "test-vm" {
					t.Errorf("vmName = %q, want %q", resp.VMName, "test-vm")
				}
			},
		},
		{
			name:   "GET request with empty fields",
			method: http.MethodGet,
			server: &Server{
				Namespace: "ns",
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, body string) {
				var resp IdentityResponse
				if err := json.Unmarshal([]byte(body), &resp); err != nil {
					t.Errorf("failed to parse response: %v", err)
					return
				}
				if resp.Namespace != "ns" {
					t.Errorf("namespace = %q, want %q", resp.Namespace, "ns")
				}
				if resp.ServiceAccountName != "" {
					t.Errorf("serviceAccountName = %q, want empty", resp.ServiceAccountName)
				}
			},
		},
		{
			name:       "POST request returns method not allowed",
			method:     http.MethodPost,
			server:     &Server{},
			wantStatus: http.StatusMethodNotAllowed,
			checkBody:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/v1/identity", nil)
			w := httptest.NewRecorder()

			tt.server.handleIdentity(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("handleIdentity() status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.checkBody != nil {
				tt.checkBody(t, w.Body.String())
			}
		})
	}
}

func TestMetadataHeaderMiddleware(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		header     string
		wantStatus int
		wantError  string
	}{
		{
			name:       "request with valid header succeeds",
			path:       "/v1/token",
			header:     "true",
			wantStatus: http.StatusOK,
			wantError:  "",
		},
		{
			name:       "request without header returns 400",
			path:       "/v1/token",
			header:     "",
			wantStatus: http.StatusBadRequest,
			wantError:  "missing_header",
		},
		{
			name:       "request with wrong header value returns 400",
			path:       "/v1/identity",
			header:     "false",
			wantStatus: http.StatusBadRequest,
			wantError:  "missing_header",
		},
		{
			name:       "healthz without header succeeds",
			path:       "/healthz",
			header:     "",
			wantStatus: http.StatusOK,
			wantError:  "",
		},
		{
			name:       "healthz with header also succeeds",
			path:       "/healthz",
			header:     "true",
			wantStatus: http.StatusOK,
			wantError:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &Server{}

			handler := server.metadataHeaderMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.header != "" {
				req.Header.Set("Metadata", tt.header)
			}
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}

			if tt.wantError != "" {
				var resp ErrorResponse
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
					t.Errorf("failed to parse error response: %v", err)
					return
				}
				if resp.Error != tt.wantError {
					t.Errorf("error = %q, want %q", resp.Error, tt.wantError)
				}
			}
		})
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	tests := []struct {
		name           string
		requestCount   int
		burstSize      int
		wantLastStatus int
		wantRetryAfter string
	}{
		{
			name:           "requests within limit succeed",
			requestCount:   5,
			burstSize:      10,
			wantLastStatus: http.StatusOK,
			wantRetryAfter: "",
		},
		{
			name:           "requests exceeding limit get 429",
			requestCount:   12,
			burstSize:      10,
			wantLastStatus: http.StatusTooManyRequests,
			wantRetryAfter: "1",
		},
		{
			name:           "exactly at limit succeeds",
			requestCount:   10,
			burstSize:      10,
			wantLastStatus: http.StatusOK,
			wantRetryAfter: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := NewServer("/tmp/token", "ns", "vm", "sa", ":0")
			// Override limiter with test values (low burst for testing)
			server.limiter = rate.NewLimiter(rate.Limit(tt.burstSize), tt.burstSize)

			handler := server.rateLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			var lastStatus int
			var lastRetryAfter string

			for i := 0; i < tt.requestCount; i++ {
				req := httptest.NewRequest(http.MethodGet, "/test", nil)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				lastStatus = w.Code
				lastRetryAfter = w.Header().Get("Retry-After")
			}

			if lastStatus != tt.wantLastStatus {
				t.Errorf("last request status = %d, want %d", lastStatus, tt.wantLastStatus)
			}
			if lastRetryAfter != tt.wantRetryAfter {
				t.Errorf("Retry-After = %q, want %q", lastRetryAfter, tt.wantRetryAfter)
			}
		})
	}
}

// createTestJWT creates a test JWT with the given claims.
// The header and signature are dummy values since we only parse the payload.
func createTestJWT(t *testing.T, claims map[string]interface{}) string {
	t.Helper()

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))

	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("failed to marshal claims: %v", err)
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)

	signature := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))

	return header + "." + encodedPayload + "." + signature
}

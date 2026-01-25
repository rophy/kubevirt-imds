package imds

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"golang.org/x/time/rate"
)

// Server is the IMDS HTTP server.
type Server struct {
	// TokenPath is the path to the ServiceAccount token file
	TokenPath string
	// Namespace is the Kubernetes namespace
	Namespace string
	// VMName is the VirtualMachine name
	VMName string
	// ServiceAccountName is the ServiceAccount name
	ServiceAccountName string
	// ListenAddr is the address to listen on (default: 169.254.169.254:80)
	ListenAddr string
	// UserData is the cloud-init user-data content (optional)
	UserData string

	server  *http.Server
	limiter *rate.Limiter
}

// NewServer creates a new IMDS server with the given configuration.
func NewServer(tokenPath, namespace, vmName, saName, listenAddr, userData string) *Server {
	if listenAddr == "" {
		listenAddr = "169.254.169.254:80"
	}

	return &Server{
		TokenPath:          tokenPath,
		Namespace:          namespace,
		VMName:             vmName,
		ServiceAccountName: saName,
		ListenAddr:         listenAddr,
		UserData:           userData,
		limiter:            rate.NewLimiter(100, 100), // 100 req/s, burst of 100
	}
}

// Run starts the IMDS server and blocks until the context is canceled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/token", s.handleToken)
	mux.HandleFunc("/v1/identity", s.handleIdentity)
	// NoCloud cloud-init endpoints
	mux.HandleFunc("/v1/meta-data", s.handleMetaData)
	mux.HandleFunc("/v1/user-data", s.handleUserData)
	mux.HandleFunc("/v1/network-config", s.handleNetworkConfig)
	// OpenStack endpoints (for cloudbase-init on Windows)
	mux.HandleFunc("/openstack/latest/meta_data.json", s.handleOpenStackMetaData)

	s.server = &http.Server{
		Addr:           s.ListenAddr,
		Handler:        s.loggingMiddleware(s.metadataHeaderMiddleware(s.rateLimitMiddleware(mux))),
		ReadTimeout:    5 * time.Second,
		WriteTimeout:   5 * time.Second,
		IdleTimeout:    20 * time.Second,
		MaxHeaderBytes: 1 << 10, // 1KB
		BaseContext:    func(net.Listener) context.Context { return ctx },
	}

	// Start server in goroutine
	errCh := make(chan error, 1)
	go func() {
		log.Printf("Starting IMDS server on %s", s.ListenAddr)
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	// Wait for context cancellation or error
	select {
	case <-ctx.Done():
		log.Println("Shutting down IMDS server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	}
}

// loggingMiddleware logs incoming requests.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

// metadataHeaderMiddleware requires the "Metadata: true" header for SSRF protection.
// This follows the same pattern as Azure IMDS.
// Exempt endpoints: /healthz (health probes), /v1/meta-data, /v1/user-data, /v1/network-config
// (cloud-init NoCloud datasource cannot send custom headers).
func (s *Server) metadataHeaderMiddleware(next http.Handler) http.Handler {
	// Paths exempt from header requirement
	// These are used by cloud-init/cloudbase-init which cannot send custom headers
	exemptPaths := map[string]bool{
		"/healthz":                        true,
		"/v1/meta-data":                   true,
		"/v1/user-data":                   true,
		"/v1/network-config":              true,
		"/openstack/latest/meta_data.json": true,
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow exempt paths without header
		if exemptPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		// Check for required header
		if r.Header.Get("Metadata") != "true" {
			s.writeError(w, http.StatusBadRequest, "missing_header", "Metadata: true header is required")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// rateLimitMiddleware enforces rate limiting (100 req/s).
func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.Allow() {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

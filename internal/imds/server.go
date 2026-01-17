package imds

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

// Server is the IMDS HTTP server.
type Server struct {
	// TokenPath is the path to the ServiceAccount token file
	TokenPath string
	// Namespace is the Kubernetes namespace
	Namespace string
	// PodName is the virt-launcher pod name
	PodName string
	// VMName is the VirtualMachine name
	VMName string
	// ServiceAccountName is the ServiceAccount name
	ServiceAccountName string
	// ListenAddr is the address to listen on (default: 169.254.169.254:80)
	ListenAddr string

	server *http.Server
}

// NewServer creates a new IMDS server with the given configuration.
func NewServer(tokenPath, namespace, podName, vmName, saName, listenAddr string) *Server {
	if listenAddr == "" {
		listenAddr = "169.254.169.254:80"
	}

	return &Server{
		TokenPath:          tokenPath,
		Namespace:          namespace,
		PodName:            podName,
		VMName:             vmName,
		ServiceAccountName: saName,
		ListenAddr:         listenAddr,
	}
}

// Run starts the IMDS server and blocks until the context is canceled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/token", s.handleToken)
	mux.HandleFunc("/v1/identity", s.handleIdentity)

	s.server = &http.Server{
		Addr:         s.ListenAddr,
		Handler:      s.loggingMiddleware(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		BaseContext:  func(net.Listener) context.Context { return ctx },
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

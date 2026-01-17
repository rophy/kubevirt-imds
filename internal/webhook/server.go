package webhook

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

var (
	scheme = runtime.NewScheme()
	codecs = serializer.NewCodecFactory(scheme)
)

func init() {
	_ = corev1.AddToScheme(scheme)
	_ = admissionv1.AddToScheme(scheme)
}

// Server is the webhook HTTP server
type Server struct {
	mutator    *Mutator
	listenAddr string
	certFile   string
	keyFile    string
	server     *http.Server
}

// NewServer creates a new webhook server
func NewServer(mutator *Mutator, listenAddr, certFile, keyFile string) *Server {
	return &Server{
		mutator:    mutator,
		listenAddr: listenAddr,
		certFile:   certFile,
		keyFile:    keyFile,
	}
}

// Run starts the webhook server
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/mutate", s.handleMutate)
	mux.HandleFunc("/healthz", s.handleHealthz)

	// Load TLS cert
	cert, err := tls.LoadX509KeyPair(s.certFile, s.keyFile)
	if err != nil {
		return fmt.Errorf("failed to load TLS cert: %w", err)
	}

	s.server = &http.Server{
		Addr:    s.listenAddr,
		Handler: mux,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
		},
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// Start server
	errCh := make(chan error, 1)
	go func() {
		log.Printf("Starting webhook server on %s", s.listenAddr)
		if err := s.server.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	// Wait for context cancellation or error
	select {
	case <-ctx.Done():
		log.Println("Shutting down webhook server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	}
}

// handleHealthz handles health check requests
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// handleMutate handles admission review requests
func (s *Server) handleMutate(w http.ResponseWriter, r *http.Request) {
	// Read body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("Failed to read request body: %v", err)
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Decode admission review
	var admissionReview admissionv1.AdmissionReview
	if _, _, err := codecs.UniversalDeserializer().Decode(body, nil, &admissionReview); err != nil {
		log.Printf("Failed to decode admission review: %v", err)
		http.Error(w, "failed to decode admission review", http.StatusBadRequest)
		return
	}

	// Process the request
	response := s.processAdmission(admissionReview.Request)

	// Build response
	admissionReview.Response = response
	admissionReview.Response.UID = admissionReview.Request.UID

	// Encode response
	respBytes, err := json.Marshal(admissionReview)
	if err != nil {
		log.Printf("Failed to encode admission review response: %v", err)
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(respBytes)
}

// processAdmission processes an admission request
func (s *Server) processAdmission(req *admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse {
	// Only handle Pod creation
	if req.Kind.Kind != "Pod" {
		return &admissionv1.AdmissionResponse{Allowed: true}
	}

	// Decode pod
	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		log.Printf("Failed to decode pod: %v", err)
		return &admissionv1.AdmissionResponse{
			Allowed: false,
			Result: &metav1.Status{
				Message: fmt.Sprintf("failed to decode pod: %v", err),
			},
		}
	}

	// Check if we should mutate
	if !s.mutator.ShouldMutate(&pod) {
		log.Printf("Pod %s/%s does not need IMDS injection", pod.Namespace, pod.Name)
		return &admissionv1.AdmissionResponse{Allowed: true}
	}

	log.Printf("Mutating pod %s/%s for IMDS injection", pod.Namespace, pod.Name)

	// Get patches
	patches, err := s.mutator.Mutate(&pod)
	if err != nil {
		log.Printf("Failed to mutate pod: %v", err)
		return &admissionv1.AdmissionResponse{
			Allowed: false,
			Result: &metav1.Status{
				Message: fmt.Sprintf("failed to mutate pod: %v", err),
			},
		}
	}

	// Create patch bytes
	patchBytes, err := CreatePatch(patches)
	if err != nil {
		log.Printf("Failed to create patch: %v", err)
		return &admissionv1.AdmissionResponse{
			Allowed: false,
			Result: &metav1.Status{
				Message: fmt.Sprintf("failed to create patch: %v", err),
			},
		}
	}

	log.Printf("Generated patch for pod %s/%s: %s", pod.Namespace, pod.Name, string(patchBytes))

	patchType := admissionv1.PatchTypeJSONPatch
	return &admissionv1.AdmissionResponse{
		Allowed:   true,
		Patch:     patchBytes,
		PatchType: &patchType,
	}
}

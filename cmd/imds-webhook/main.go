package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	corev1 "k8s.io/api/core/v1"

	"github.com/kubevirt/kubevirt-imds/pkg/webhook"
)

func main() {
	var (
		listenAddr string
		certFile   string
		keyFile    string
		imdsImage  string
	)

	flag.StringVar(&listenAddr, "listen-addr", ":8443", "Address to listen on")
	flag.StringVar(&certFile, "cert-file", "/etc/webhook/certs/tls.crt", "Path to TLS certificate")
	flag.StringVar(&keyFile, "key-file", "/etc/webhook/certs/tls.key", "Path to TLS key")
	flag.StringVar(&imdsImage, "imds-image", "", "IMDS sidecar image (required)")
	flag.Parse()

	// Allow overriding from environment
	if v := os.Getenv("IMDS_IMAGE"); v != "" {
		imdsImage = v
	}

	if imdsImage == "" {
		log.Fatal("--imds-image or IMDS_IMAGE is required")
	}

	// Create mutator
	config := webhook.Config{
		IMDSImage:       imdsImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
	}
	mutator := webhook.NewMutator(config)

	// Create server
	server := webhook.NewServer(mutator, listenAddr, certFile, keyFile)

	// Set up signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		cancel()
	}()

	// Run server
	if err := server.Run(ctx); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

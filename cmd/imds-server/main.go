package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kubevirt/kubevirt-imds/internal/imds"
	"github.com/kubevirt/kubevirt-imds/internal/network"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <command>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  init   - Set up veth pair and attach to bridge\n")
		fmt.Fprintf(os.Stderr, "  serve  - Start IMDS HTTP server\n")
		fmt.Fprintf(os.Stderr, "  run    - Wait for bridge, set up veth, then serve (for sidecar use)\n")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "init":
		if err := runInit(); err != nil {
			log.Fatalf("Init failed: %v", err)
		}
	case "serve":
		if err := runServe(); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	case "run":
		if err := runAll(); err != nil {
			log.Fatalf("Run failed: %v", err)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

// runInit sets up the veth pair and attaches it to the VM bridge.
func runInit() error {
	// Get bridge name from env or auto-detect
	bridgeName := os.Getenv("IMDS_BRIDGE_NAME")
	if bridgeName == "" {
		var err error
		bridgeName, err = network.DiscoverBridge()
		if err != nil {
			return fmt.Errorf("failed to discover bridge: %w", err)
		}
		log.Printf("Auto-detected bridge: %s", bridgeName)
	} else {
		log.Printf("Using configured bridge: %s", bridgeName)
	}

	// Clean up any existing veth pair
	if err := network.CleanupVeth(); err != nil {
		log.Printf("Warning: failed to cleanup existing veth: %v", err)
	}

	// Set up the veth pair
	if err := network.SetupVeth(bridgeName); err != nil {
		return fmt.Errorf("failed to setup veth: %w", err)
	}

	log.Printf("Successfully set up veth pair attached to bridge %s", bridgeName)
	log.Printf("IMDS will be available at %s", network.IMDSAddress)
	return nil
}

// runServe starts the IMDS HTTP server.
func runServe() error {
	// Read configuration from environment
	tokenPath := getEnvOrDefault("IMDS_TOKEN_PATH", "/var/run/secrets/tokens/token")
	namespace := os.Getenv("IMDS_NAMESPACE")
	podName := os.Getenv("IMDS_POD_NAME")
	vmName := os.Getenv("IMDS_VM_NAME")
	saName := os.Getenv("IMDS_SA_NAME")
	listenAddr := getEnvOrDefault("IMDS_LISTEN_ADDR", "169.254.169.254:80")

	if namespace == "" {
		return fmt.Errorf("IMDS_NAMESPACE is required")
	}

	server := imds.NewServer(tokenPath, namespace, podName, vmName, saName, listenAddr)

	// Set up signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		cancel()
	}()

	return server.Run(ctx)
}

// runAll waits for the bridge to be created, sets up veth, then runs the server.
// This is the main entry point for the sidecar container.
func runAll() error {
	log.Println("Starting IMDS sidecar (waiting for VM bridge...)")

	// Wait for the bridge to be created (with timeout)
	bridgeName := os.Getenv("IMDS_BRIDGE_NAME")
	timeout := 5 * time.Minute
	pollInterval := 2 * time.Second
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		var err error
		if bridgeName == "" {
			bridgeName, err = network.DiscoverBridge()
			if err == nil {
				log.Printf("Found bridge: %s", bridgeName)
				break
			}
		} else {
			_, err = network.GetBridge(bridgeName)
			if err == nil {
				log.Printf("Bridge %s is ready", bridgeName)
				break
			}
		}

		log.Printf("Waiting for bridge... (%v)", err)
		time.Sleep(pollInterval)
		bridgeName = "" // Reset for next auto-detect attempt
	}

	if bridgeName == "" {
		return fmt.Errorf("timed out waiting for VM bridge after %v", timeout)
	}

	// Set up veth
	if err := network.CleanupVeth(); err != nil {
		log.Printf("Warning: failed to cleanup existing veth: %v", err)
	}

	if err := network.SetupVeth(bridgeName); err != nil {
		return fmt.Errorf("failed to setup veth: %w", err)
	}

	log.Printf("Successfully set up veth pair attached to bridge %s", bridgeName)

	// Now run the server
	return runServe()
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

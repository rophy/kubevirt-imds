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

	// Ensure veth pair exists and is configured correctly
	if err := network.EnsureVeth(bridgeName); err != nil {
		return fmt.Errorf("failed to ensure veth: %w", err)
	}

	log.Printf("Successfully ensured veth pair attached to bridge %s", bridgeName)
	log.Printf("IMDS will be available at %s", network.IMDSAddress)
	return nil
}

// runServe starts the IMDS HTTP server with its own signal handling.
func runServe() error {
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

	return runServeWithContext(ctx)
}

// runServeWithContext starts the IMDS HTTP server with the provided context.
func runServeWithContext(ctx context.Context) error {
	// Read configuration from environment
	tokenPath := getEnvOrDefault("IMDS_TOKEN_PATH", "/var/run/secrets/tokens/token")
	namespace := os.Getenv("IMDS_NAMESPACE")
	vmName := os.Getenv("IMDS_VM_NAME")
	saName := os.Getenv("IMDS_SA_NAME")
	listenAddr := getEnvOrDefault("IMDS_LISTEN_ADDR", "169.254.169.254:80")
	userData := os.Getenv("IMDS_USER_DATA")

	if namespace == "" {
		return fmt.Errorf("IMDS_NAMESPACE is required")
	}

	server := imds.NewServer(tokenPath, namespace, vmName, saName, listenAddr, userData)
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

	// Ensure veth pair exists and is configured correctly
	if err := network.EnsureVeth(bridgeName); err != nil {
		return fmt.Errorf("failed to ensure veth: %w", err)
	}

	log.Printf("Successfully ensured veth pair attached to bridge %s", bridgeName)

	// Discover VM's MAC address
	// For masquerade mode: uses pod's eth0 MAC (VM shares this MAC)
	// For bridge mode: uses tap device MAC
	vmMAC, err := network.DiscoverVMMAC(bridgeName)
	if err != nil {
		return fmt.Errorf("failed to discover VM MAC: %w", err)
	}
	log.Printf("Discovered VM MAC: %s", vmMAC)

	// Start ARP responder for link-local IMDS access
	// This allows VMs with only link-local addresses (no DHCP) to reach IMDS
	// Only responds to requests from the VM's MAC for security
	arpResponder, err := network.NewARPResponder(bridgeName, vmMAC)
	if err != nil {
		return fmt.Errorf("failed to create ARP responder: %w", err)
	}

	// Set up context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		cancel()
	}()

	// Run ARP responder in background
	go func() {
		if err := arpResponder.Run(ctx); err != nil && err != context.Canceled {
			log.Printf("ARP responder error: %v", err)
		}
	}()

	// Run the HTTP server
	return runServeWithContext(ctx)
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

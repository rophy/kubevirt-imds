package network

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

const (
	// VethIMDS is the name of the veth interface where IMDS listens
	VethIMDS = "veth-imds"
	// VethIMDSBridge is the name of the veth interface attached to the bridge
	VethIMDSBridge = "veth-imds-br"
	// IMDSAddress is the link-local IP address for IMDS
	IMDSAddress = "169.254.169.254"
)

// SetupVeth creates a veth pair and attaches one end to the specified bridge.
// The other end is configured with the IMDS IP address (169.254.169.254).
func SetupVeth(bridgeName string) error {
	// Get the bridge
	bridge, err := GetBridge(bridgeName)
	if err != nil {
		return err
	}

	// Create veth pair
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{
			Name: VethIMDS,
		},
		PeerName: VethIMDSBridge,
	}

	if err := netlink.LinkAdd(veth); err != nil {
		return fmt.Errorf("failed to create veth pair: %w", err)
	}

	// Get the bridge-side veth
	vethBr, err := netlink.LinkByName(VethIMDSBridge)
	if err != nil {
		return fmt.Errorf("failed to get %s: %w", VethIMDSBridge, err)
	}

	// Attach bridge-side veth to the bridge
	if err := netlink.LinkSetMaster(vethBr, bridge); err != nil {
		return fmt.Errorf("failed to attach %s to bridge %s: %w", VethIMDSBridge, bridgeName, err)
	}

	// Bring up the bridge-side veth
	if err := netlink.LinkSetUp(vethBr); err != nil {
		return fmt.Errorf("failed to bring up %s: %w", VethIMDSBridge, err)
	}

	// Get the IMDS-side veth
	vethIMDS, err := netlink.LinkByName(VethIMDS)
	if err != nil {
		return fmt.Errorf("failed to get %s: %w", VethIMDS, err)
	}

	// Add IMDS IP address to the IMDS-side veth
	addr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   net.ParseIP(IMDSAddress),
			Mask: net.CIDRMask(32, 32),
		},
	}
	if err := netlink.AddrAdd(vethIMDS, addr); err != nil {
		return fmt.Errorf("failed to add address %s to %s: %w", IMDSAddress, VethIMDS, err)
	}

	// Bring up the IMDS-side veth
	if err := netlink.LinkSetUp(vethIMDS); err != nil {
		return fmt.Errorf("failed to bring up %s: %w", VethIMDS, err)
	}

	return nil
}

// CleanupVeth removes the veth pair if it exists.
func CleanupVeth() error {
	link, err := netlink.LinkByName(VethIMDS)
	if err != nil {
		// Link doesn't exist, nothing to clean up
		return nil
	}

	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("failed to delete %s: %w", VethIMDS, err)
	}

	return nil
}

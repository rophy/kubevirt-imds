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

// EnsureVeth validates existing veth pair or creates a new one.
// This preserves the MAC address across restarts to avoid ARP cache issues.
func EnsureVeth(bridgeName string) error {
	// Get the bridge first
	bridge, err := GetBridge(bridgeName)
	if err != nil {
		return err
	}

	// Check if veth already exists
	vethIMDS, err := netlink.LinkByName(VethIMDS)
	if err != nil {
		// Doesn't exist, create new
		return SetupVeth(bridgeName)
	}

	// veth exists, validate and fix if needed
	vethBr, err := netlink.LinkByName(VethIMDSBridge)
	if err != nil {
		// Bridge side missing (shouldn't happen), recreate
		CleanupVeth()
		return SetupVeth(bridgeName)
	}

	// Check if attached to correct bridge
	if !isAttachedToBridge(vethBr, bridge) {
		// Wrong bridge, recreate
		CleanupVeth()
		return SetupVeth(bridgeName)
	}

	// Ensure IP address is configured
	if err := ensureIPAddress(vethIMDS); err != nil {
		return err
	}

	// Ensure both interfaces are UP
	if err := netlink.LinkSetUp(vethBr); err != nil {
		return fmt.Errorf("failed to bring up %s: %w", VethIMDSBridge, err)
	}
	if err := netlink.LinkSetUp(vethIMDS); err != nil {
		return fmt.Errorf("failed to bring up %s: %w", VethIMDS, err)
	}

	return nil
}

// isAttachedToBridge checks if the link is attached to the specified bridge.
func isAttachedToBridge(link netlink.Link, bridge netlink.Link) bool {
	return link.Attrs().MasterIndex == bridge.Attrs().Index
}

// ensureIPAddress ensures the IMDS IP address is configured on the interface.
func ensureIPAddress(link netlink.Link) error {
	expectedAddr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   net.ParseIP(IMDSAddress),
			Mask: net.CIDRMask(32, 32),
		},
	}

	// Check existing addresses
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("failed to list addresses on %s: %w", link.Attrs().Name, err)
	}

	for _, addr := range addrs {
		if addr.IP.Equal(expectedAddr.IP) {
			// IP already configured
			return nil
		}
	}

	// IP not found, add it
	if err := netlink.AddrAdd(link, expectedAddr); err != nil {
		return fmt.Errorf("failed to add address %s to %s: %w", IMDSAddress, link.Attrs().Name, err)
	}

	return nil
}

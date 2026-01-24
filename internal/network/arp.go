package network

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sync"
	"syscall"

	"github.com/vishvananda/netlink"
)

const (
	// ARP protocol constants
	arpHardwareTypeEthernet = 1
	arpProtocolTypeIPv4     = 0x0800
	arpOpRequest            = 1
	arpOpReply              = 2
	ethernetHeaderLen       = 14
	arpPacketLen            = 28
)

// ARPResponder listens for ARP requests on a bridge interface and responds
// to requests for the IMDS IP address (169.254.169.254).
// This enables VMs with only link-local addresses to reach IMDS via L2.
type ARPResponder struct {
	bridgeName string
	imdsIP     net.IP
	imdsMAC    net.HardwareAddr
	fd         int
	mu         sync.Mutex
	running    bool
}

// NewARPResponder creates a new ARP responder for the given bridge.
// It will respond to ARP requests for the IMDS IP using the MAC address
// of the veth-imds interface.
func NewARPResponder(bridgeName string) (*ARPResponder, error) {
	// Get the MAC address of veth-imds
	vethIMDS, err := netlink.LinkByName(VethIMDS)
	if err != nil {
		return nil, fmt.Errorf("failed to get %s: %w", VethIMDS, err)
	}

	imdsMAC := vethIMDS.Attrs().HardwareAddr
	if len(imdsMAC) == 0 {
		return nil, fmt.Errorf("%s has no MAC address", VethIMDS)
	}

	return &ARPResponder{
		bridgeName: bridgeName,
		imdsIP:     net.ParseIP(IMDSAddress).To4(),
		imdsMAC:    imdsMAC,
	}, nil
}

// Run starts the ARP responder. It blocks until the context is cancelled.
func (a *ARPResponder) Run(ctx context.Context) error {
	// Get the bridge interface itself to see all ARP broadcasts
	// We must listen on the bridge (not a bridge port) to see traffic from all ports
	bridge, err := netlink.LinkByName(a.bridgeName)
	if err != nil {
		return fmt.Errorf("failed to get bridge %s: %w", a.bridgeName, err)
	}

	// Create a raw socket bound to the bridge to capture ARP packets
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(syscall.ETH_P_ARP)))
	if err != nil {
		return fmt.Errorf("failed to create raw socket: %w", err)
	}

	a.mu.Lock()
	a.fd = fd
	a.running = true
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		a.running = false
		syscall.Close(a.fd)
		a.mu.Unlock()
	}()

	// Bind to the bridge interface to see all broadcast traffic
	addr := syscall.SockaddrLinklayer{
		Protocol: htons(syscall.ETH_P_ARP),
		Ifindex:  bridge.Attrs().Index,
	}
	if err := syscall.Bind(fd, &addr); err != nil {
		return fmt.Errorf("failed to bind to bridge %s: %w", a.bridgeName, err)
	}

	log.Printf("ARP responder listening on bridge %s for %s", a.bridgeName, IMDSAddress)

	// Read ARP packets
	buf := make([]byte, 1500)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Set read deadline to allow checking context
		tv := syscall.Timeval{Sec: 1, Usec: 0}
		syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK || err == syscall.EINTR {
				continue // Timeout or interrupted, check context and retry
			}
			return fmt.Errorf("failed to read from socket: %w", err)
		}

		if n < ethernetHeaderLen+arpPacketLen {
			continue // Packet too small
		}

		// Parse and handle ARP request
		a.handlePacket(fd, buf[:n], bridge.Attrs().Index)
	}
}

// handlePacket processes an ARP packet and sends a reply if it's a request for IMDS IP.
func (a *ARPResponder) handlePacket(fd int, packet []byte, ifindex int) {
	// Skip Ethernet header (14 bytes)
	arp := packet[ethernetHeaderLen:]

	// Check ARP header
	if len(arp) < arpPacketLen {
		return
	}

	hardwareType := binary.BigEndian.Uint16(arp[0:2])
	protocolType := binary.BigEndian.Uint16(arp[2:4])
	hardwareLen := arp[4]
	protocolLen := arp[5]
	operation := binary.BigEndian.Uint16(arp[6:8])

	// Validate ARP packet
	if hardwareType != arpHardwareTypeEthernet ||
		protocolType != arpProtocolTypeIPv4 ||
		hardwareLen != 6 ||
		protocolLen != 4 ||
		operation != arpOpRequest {
		return
	}

	// Extract sender and target info
	senderMAC := net.HardwareAddr(arp[8:14])
	senderIP := net.IP(arp[14:18])
	targetIP := net.IP(arp[24:28])

	// Check if this is a request for the IMDS IP
	if !targetIP.Equal(a.imdsIP) {
		return
	}

	log.Printf("ARP request for %s from %s (%s)", targetIP, senderIP, senderMAC)

	// Build ARP reply
	reply := a.buildARPReply(senderMAC, senderIP)

	// Send the reply
	destAddr := syscall.SockaddrLinklayer{
		Protocol: htons(syscall.ETH_P_ARP),
		Ifindex:  ifindex,
		Halen:    6,
	}
	copy(destAddr.Addr[:], senderMAC)

	if err := syscall.Sendto(fd, reply, 0, &destAddr); err != nil {
		log.Printf("Failed to send ARP reply: %v", err)
		return
	}

	log.Printf("ARP reply sent: %s is at %s", a.imdsIP, a.imdsMAC)
}

// buildARPReply constructs an ARP reply packet.
func (a *ARPResponder) buildARPReply(destMAC net.HardwareAddr, destIP net.IP) []byte {
	packet := make([]byte, ethernetHeaderLen+arpPacketLen)

	// Ethernet header
	copy(packet[0:6], destMAC)         // Destination MAC
	copy(packet[6:12], a.imdsMAC)      // Source MAC
	binary.BigEndian.PutUint16(packet[12:14], syscall.ETH_P_ARP)

	// ARP header
	arp := packet[ethernetHeaderLen:]
	binary.BigEndian.PutUint16(arp[0:2], arpHardwareTypeEthernet)
	binary.BigEndian.PutUint16(arp[2:4], arpProtocolTypeIPv4)
	arp[4] = 6 // Hardware address length
	arp[5] = 4 // Protocol address length
	binary.BigEndian.PutUint16(arp[6:8], arpOpReply)

	// Sender (IMDS)
	copy(arp[8:14], a.imdsMAC)
	copy(arp[14:18], a.imdsIP.To4())

	// Target (requesting host)
	copy(arp[18:24], destMAC)
	copy(arp[24:28], destIP.To4())

	return packet
}

// Stop gracefully stops the ARP responder.
func (a *ARPResponder) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.running && a.fd > 0 {
		syscall.Close(a.fd)
		a.running = false
	}
}

// htons converts a uint16 from host to network byte order.
func htons(v uint16) uint16 {
	return (v << 8) | (v >> 8)
}

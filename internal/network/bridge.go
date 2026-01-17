package network

import (
	"fmt"
	"strings"

	"github.com/vishvananda/netlink"
)

// DiscoverBridge finds the KubeVirt VM bridge.
// KubeVirt creates bridges with names like k6t-eth0, k6t-net0, etc.
func DiscoverBridge() (string, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return "", fmt.Errorf("failed to list network links: %w", err)
	}

	for _, link := range links {
		if link.Type() == "bridge" && strings.HasPrefix(link.Attrs().Name, "k6t-") {
			return link.Attrs().Name, nil
		}
	}

	return "", fmt.Errorf("no KubeVirt bridge (k6t-*) found")
}

// GetBridge returns a bridge by name.
func GetBridge(name string) (netlink.Link, error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get bridge %s: %w", name, err)
	}

	if link.Type() != "bridge" {
		return nil, fmt.Errorf("%s is not a bridge (type: %s)", name, link.Type())
	}

	return link, nil
}

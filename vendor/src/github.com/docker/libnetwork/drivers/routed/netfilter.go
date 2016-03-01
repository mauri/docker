package routed

import (
	"fmt"
	"net"

	"github.com/Sirupsen/logrus"
	"github.com/docker/libnetwork/iptables"
	"github.com/docker/libnetwork/netlabel"
)

const (
	containersChainName  = "CONTAINERS"
	containerChainPrefix = "CONTAINER-"
)

type netFilter struct {
	ifaceName      string
	ingressAllowed []*net.IPNet
}

func NewNetFilter(ifaceName string, epOptions map[string]interface{}) *netFilter {
	logrus.Debugf("New NetFilter for iface %s and options %s", ifaceName, epOptions)

	ingressFiltering := epOptions[netlabel.IngressAllowed].([]*net.IPNet)
	if ingressFiltering == nil {
		// TODO we might want to throw an exception in the future (make filtering mandatory)
		logrus.Info("No network ingress filtering specified")
	}

	return &netFilter{ifaceName, ingressFiltering}
}

func (n *netFilter) applyFiltering() error {
	if n.ingressAllowed == nil {
		return nil // No holes to poke
	}

	containerChainName := containerChainPrefix + n.ifaceName

	logrus.Debugf("NetFilter. Allowing ingress: %s", n.ingressAllowed)

	// TODO Verify "CONTAINERS" chain exist (and has references)
	// TODO Verify "hostIfaceName" chain doesn't exist

	// Create "hostIfaceName" chain
	if err := callIptables("-N", containerChainName); err != nil {
		return err
	}

	// Allow specified ranges only
	for _, ipNet := range n.ingressAllowed {
		if err := callIptables("-A", containerChainName, "-s", ipNet.String(), "-j", "ACCEPT"); err != nil {
			return err
		}
	}

	if err := callIptables("-A", containerChainName, "-j", "RETURN"); err != nil {
		return err
	}

	// Add JUMP in CONTAINERS, send all traffic going to the veth interface
	if err := callIptables("-I", containersChainName, "1", "-o", n.ifaceName, "-j", containerChainName); err != nil {
		return err
	}

	logrus.Info("NetFilter: Successfully applied ingress filtering")

	return nil
}

func (n *netFilter) removeFiltering() error {
	logrus.Debugf("NetFilter. Removing rules for %s", n.ifaceName)

	containerChainName := containerChainPrefix + n.ifaceName

	if err := callIptables("-D", containersChainName, "-o", n.ifaceName, "-j", containerChainName); err != nil {
		return err
	}

	if err := callIptables("-F", containerChainName); err != nil {
		return err
	}

	if err := callIptables("-X", containerChainName); err != nil {
		return err
	}

	return nil
}

func callIptables(args ...string) error {
	logrus.Debugf("NetFilter. IpTables call %s", args)
	if output, err := iptables.Raw(args...); err != nil {
		return fmt.Errorf("IP tables set up failed %s %s %v", args, output, err)
	}
	return nil
}

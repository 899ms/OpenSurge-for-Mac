package controlapi

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"open-mihomo-gateway/internal/config"
	"open-mihomo-gateway/internal/mihomo"
)

// gatewayLocalIdentity describes network identities owned by the Mac. Process
// metadata is optional in Mihomo and must not determine connection ownership.
type gatewayLocalIdentity struct {
	gatewayIP  string
	tunEnabled bool
	tunSources map[string]struct{}
}

func newGatewayLocalIdentity(gatewayIP string, tun mihomo.TUNRuntimeState) gatewayLocalIdentity {
	identity := gatewayLocalIdentity{
		gatewayIP:  normalizeTrafficIP(gatewayIP),
		tunEnabled: tun.Enabled,
		tunSources: map[string]struct{}{},
	}
	if tun.Enabled {
		for _, addresses := range [][]string{tun.IPv4Addresses, tun.IPv6Addresses} {
			for _, value := range addresses {
				prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
				if err != nil || prefix.Addr().IsUnspecified() || prefix.Addr().IsMulticast() {
					continue
				}
				// The interface address is local, not every address in its subnet.
				identity.tunSources[prefix.Addr().Unmap().String()] = struct{}{}
			}
		}
	}
	return identity
}

func (s *Server) fetchGatewayLocalIdentity(ctx context.Context, cfg config.Config, snapshot mihomo.ConnectionsSnapshot) (gatewayLocalIdentity, error) {
	identity := newGatewayLocalIdentity(cfg.Gateway.LANIP, mihomo.TUNRuntimeState{})
	for _, connection := range snapshot.Connections {
		if !isSystemTUNConnection(connection) || hasDownstreamInboundIdentity(connection) {
			continue
		}
		// Use the live addresses, including the effective IPv6 address after
		// auto detection. Desired settings and old process evidence can be stale.
		tun, err := s.fetchTUNRuntime(ctx, cfg)
		if err != nil {
			return identity, fmt.Errorf("read Mac system TUN identity: %w", err)
		}
		identity = newGatewayLocalIdentity(cfg.Gateway.LANIP, tun)
		if tun.Enabled && len(identity.tunSources) == 0 {
			return identity, fmt.Errorf("mihomo runtime did not report system TUN source addresses")
		}
		return identity, nil
	}
	return identity, nil
}

func (identity gatewayLocalIdentity) matches(connection mihomo.Connection) bool {
	if hasDownstreamInboundIdentity(connection) {
		return false
	}
	sourceIP := normalizeTrafficIP(metadataString(connection.Metadata, "sourceIP"))
	if sourceIP == "" {
		return false
	}
	if localConnectionTransport(connection) == localTransportTUN {
		if !identity.tunEnabled || !isSystemTUNConnection(connection) {
			return false
		}
		if _, exists := identity.tunSources[sourceIP]; exists {
			return true
		}
	}
	return sourceIP == identity.gatewayIP || isLoopbackIP(sourceIP)
}

func isSystemTUNConnection(connection mihomo.Connection) bool {
	return localConnectionTransport(connection) == localTransportTUN &&
		metadataString(connection.Metadata, "inboundName") == mihomo.SystemTUNListenerName
}

func hasDownstreamInboundIdentity(connection mihomo.Connection) bool {
	return metadataString(connection.Metadata, "inboundName") == config.IPv6PacketListenerName ||
		strings.HasPrefix(metadataString(connection.Metadata, "inboundUser"), "device:")
}

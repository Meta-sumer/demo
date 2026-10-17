package httpx

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/logx"
)

// Client address.
//
// X-Forwarded-For and X-Real-IP are ordinary request headers: anyone can send
// them. They are believed only when the connection itself comes from a proxy
// listed in TRUSTED_PROXIES, and then X-Forwarded-For is read from the right
// (the hop the last trusted proxy saw) leftwards, skipping further trusted
// proxies; the first address that is not a trusted proxy is the client. Every
// IP-keyed rate limit, the trade record's ip and the audit trail use this.

// ClientIPFrom derives the client address from the direct peer and the
// forwarding headers, given the trusted proxy ranges.
func ClientIPFrom(remoteAddr string, header http.Header, trusted []netip.Prefix) string {
	peer, ok := parseHostAddr(remoteAddr)
	if !ok {
		return remoteAddr
	}
	if !isTrusted(peer, trusted) {
		if header.Get("X-Forwarded-For") != "" || header.Get("X-Real-Ip") != "" {
			warnUntrustedForwarder(peer)
		}
		return peer.String()
	}

	if xff := header.Values("X-Forwarded-For"); len(xff) > 0 {
		hops := []string{}
		for _, line := range xff {
			for _, part := range strings.Split(line, ",") {
				if p := strings.TrimSpace(part); p != "" {
					hops = append(hops, p)
				}
			}
		}
		last := peer
		for i := len(hops) - 1; i >= 0; i-- {
			addr, ok := parseHostAddr(hops[i])
			if !ok {
				// Garbage can only have come from the untrusted side, so the
				// last address a trusted hop vouched for is the best we have.
				return last.String()
			}
			if !isTrusted(addr, trusted) {
				return addr.String()
			}
			last = addr
		}
		if len(hops) > 0 {
			// Every hop is a trusted proxy: the left-most is the origin.
			return last.String()
		}
	}
	if real := strings.TrimSpace(header.Get("X-Real-Ip")); real != "" {
		if addr, ok := parseHostAddr(real); ok {
			return addr.String()
		}
	}
	return peer.String()
}

// parseHostAddr accepts "ip", "ip:port", "[v6]" and "[v6]:port".
func parseHostAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
		return a.Unmap().WithZone(""), true
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		if a, err := netip.ParseAddr(host); err == nil {
			return a.Unmap().WithZone(""), true
		}
	}
	return netip.Addr{}, false
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

var (
	warnedMu    sync.Mutex
	warnedPeers = map[netip.Addr]bool{}
)

// warnUntrustedForwarder logs once per peer when a private-network peer sends
// forwarding headers: almost always a reverse proxy that TRUSTED_PROXIES does
// not list yet, which would put every client behind it in one rate-limit
// bucket.
func warnUntrustedForwarder(peer netip.Addr) {
	if !peer.IsPrivate() && !peer.IsLoopback() {
		return
	}
	warnedMu.Lock()
	defer warnedMu.Unlock()
	if warnedPeers[peer] || len(warnedPeers) >= 64 {
		return
	}
	warnedPeers[peer] = true
	logx.Warn("ignoring X-Forwarded-For from a peer that is not in TRUSTED_PROXIES - if this is your reverse proxy, add it",
		"peer", peer.String())
}

// clientIP is ClientIP's implementation, with the configured proxies.
func clientIP(r *http.Request) string {
	return ClientIPFrom(r.RemoteAddr, r.Header, config.TrustedProxyPrefixes())
}

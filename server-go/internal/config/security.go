package config

import (
	"fmt"
	"net/netip"
	"strings"
)

// Security settings: trusted proxies, admin two-factor authentication and the
// seed passwords. Kept apart from env.go so the hardening is easy to review.

// DefaultTrustedProxies is loopback only: a header that claims to know the
// client's address is believed only from a proxy on the same host.
const DefaultTrustedProxies = "127.0.0.1/32,::1/128"

var defaultTrustedPrefixes = mustPrefixes(DefaultTrustedProxies)

func mustPrefixes(raw string) []netip.Prefix {
	p, err := ParseTrustedProxies(raw)
	if err != nil {
		panic(err)
	}
	return p
}

// ParseTrustedProxies reads a comma or whitespace separated list of CIDR
// ranges or bare addresses ("none" trusts nothing).
func ParseTrustedProxies(raw string) ([]netip.Prefix, error) {
	out := []netip.Prefix{}
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
	if len(fields) == 1 && strings.EqualFold(fields[0], "none") {
		return out, nil
	}
	for _, f := range fields {
		if strings.Contains(f, "/") {
			p, err := netip.ParsePrefix(f)
			if err != nil {
				return nil, fmt.Errorf("%q is not a CIDR range", f)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, fmt.Errorf("%q is not an IP address or CIDR range", f)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// TrustedProxyPrefixes is the configured list, or loopback when the
// configuration was built without it (tests that assemble config.E by hand).
func TrustedProxyPrefixes() []netip.Prefix {
	if E == nil || E.TrustedProxies == nil {
		return defaultTrustedPrefixes
	}
	return E.TrustedProxies
}

// Admin2FAEnforced reports ADMIN_2FA_REQUIRED (true when unset or when the
// configuration is not loaded: an unconfigured process must not relax it).
func Admin2FAEnforced() bool {
	if E == nil {
		return true
	}
	return E.Admin2FARequired
}

func loadSecurity(l *loader, e *Env) {
	raw := l.str("TRUSTED_PROXIES", DefaultTrustedProxies)
	prefixes, err := ParseTrustedProxies(raw)
	if err != nil {
		l.fail("TRUSTED_PROXIES", err.Error())
		prefixes = defaultTrustedPrefixes
	}
	e.TrustedProxies = prefixes

	e.Admin2FARequired = l.boolean("ADMIN_2FA_REQUIRED", true)
	// The issuer is the label authenticator apps show; ':' separates the
	// issuer from the account in an otpauth label, so it cannot appear here.
	e.Admin2FAIssuer = strings.TrimSpace(strings.ReplaceAll(l.str("ADMIN_2FA_ISSUER", "Binary Trading Admin"), ":", " "))
	if e.Admin2FAIssuer == "" {
		e.Admin2FAIssuer = "Binary Trading Admin"
	}
	e.MFAEncryptionKey = l.str("MFA_ENCRYPTION_KEY", "")
	if e.MFAEncryptionKey != "" && len(e.MFAEncryptionKey) < 32 {
		l.fail("MFA_ENCRYPTION_KEY", "MFA_ENCRYPTION_KEY must be at least 32 characters")
	}

	e.SeedRiskPassword = l.str("SEED_RISK_PASSWORD", "")
	e.SeedFinancePassword = l.str("SEED_FINANCE_PASSWORD", "")
	e.SeedTraderPassword = l.str("SEED_TRADER_PASSWORD", "")

	if e.NodeEnv == "production" && strings.HasPrefix(e.JWTRefreshSecret, "change-me") {
		l.fail("JWT_REFRESH_SECRET", "Default development secrets cannot be used in production")
	}
}

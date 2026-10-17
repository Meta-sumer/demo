package config

import (
	"net/netip"
	"testing"
)

func TestParseTrustedProxies(t *testing.T) {
	p, err := ParseTrustedProxies("127.0.0.1/32, ::1/128 172.18.0.1\t10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"127.0.0.1/32", "::1/128", "172.18.0.1/32", "10.0.0.0/8"}
	if len(p) != len(want) {
		t.Fatalf("got %v", p)
	}
	for i, w := range want {
		if p[i].String() != w {
			t.Errorf("entry %d: %s, want %s", i, p[i], w)
		}
	}
	if none, err := ParseTrustedProxies("none"); err != nil || len(none) != 0 {
		t.Fatalf("none: %v %v", none, err)
	}
	for _, bad := range []string{"10.0.0.0/33", "proxy.local", "*"} {
		if _, err := ParseTrustedProxies(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestTrustedProxyDefaultIsLoopbackOnly(t *testing.T) {
	prev := E
	t.Cleanup(func() { E = prev })
	E = nil
	got := TrustedProxyPrefixes()
	if len(got) != 2 || !got[0].Contains(netip.MustParseAddr("127.0.0.1")) || got[0].Contains(netip.MustParseAddr("172.18.0.1")) {
		t.Fatalf("default %v", got)
	}
	if !Admin2FAEnforced() {
		t.Fatal("2FA must be enforced when the configuration is not loaded")
	}
}

func TestRemovedPermissionsAreDropped(t *testing.T) {
	for _, p := range []string{"market-data:write", "settings:read", "admin:manage"} {
		if IsValidPermission(p) {
			t.Errorf("%s still declared", p)
		}
	}
	got := PermissionsForRole(RoleSupport, []string{"admin:manage", PermExposureRead})
	for _, p := range got {
		if p == "admin:manage" {
			t.Fatal("a removed permission survived PermissionsForRole")
		}
	}
	if !HasPermission(RoleAdmin, nil, PermUsersSuspend) || HasPermission(RoleSupport, nil, PermUsersSuspend) {
		t.Fatal("users:suspend: ADMIN must hold it, SUPPORT must not")
	}
	if HasPermission(RoleSupport, nil, PermExposureRead) || HasPermission(RoleSupport, nil, PermReportsRead) {
		t.Fatal("SUPPORT must not read exposure or P&L")
	}
}

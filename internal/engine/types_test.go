package engine

import "testing"

func TestResolveDefaultPortsPreservesExplicitOfficialPort(t *testing.T) {
	official, err := ExpandCIDR("104.16.0.0/24", SampleOnePerSubnet, 1, 2053)
	if err != nil {
		t.Fatalf("ExpandCIDR failed: %v", err)
	}
	resolved := ResolveDefaultPorts(official, true)
	if len(resolved) != 1 || resolved[0].Port != 2053 {
		t.Fatalf("explicit official port must win over TLS fallback: %+v", resolved)
	}
}

func TestResolveDefaultPortsOnlyFillsMissingPort(t *testing.T) {
	targets := []Target{{IP: "1.1.1.1"}, {IP: "1.0.0.1", Port: 8443}}
	resolved := ResolveDefaultPorts(targets, true)
	if got := resolved[0].Port; got != 443 {
		t.Fatalf("missing TLS port = %d, want 443", got)
	}
	if got := resolved[1].Port; got != 8443 {
		t.Fatalf("explicit port = %d, want 8443", got)
	}
}

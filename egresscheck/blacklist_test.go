package egresscheck

import (
	"net/netip"
	"testing"
)

func TestBlacklistMatch(t *testing.T) {
	rules := []string{
		"139.144.196.179",
		"185.0.0.0/8",
		"139.139.*",
	}

	var parsed []blacklistRule
	for _, raw := range rules {
		rule, err := parseRule(raw)
		if err != nil {
			t.Fatalf("parseRule(%q): %v", raw, err)
		}
		parsed = append(parsed, rule)
	}

	blacklist := &Blacklist{rules: parsed}
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{name: "exact", ip: "139.144.196.179", want: true},
		{name: "cidr", ip: "185.10.20.30", want: true},
		{name: "prefix", ip: "139.139.1.2", want: true},
		{name: "miss", ip: "104.28.208.133", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := blacklist.Match(netip.MustParseAddr(tt.ip))
			if got != tt.want {
				t.Fatalf("Match(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestParseRuleRejectsComplexWildcard(t *testing.T) {
	if _, err := parseRule("139.*.1.*"); err == nil {
		t.Fatal("expected complex wildcard to be rejected")
	}
}

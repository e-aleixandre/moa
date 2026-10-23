package voice

import (
	"net"
	"net/netip"
	"strings"
	"testing"
)

func TestInterfaceClasses(t *testing.T) {
	var table []diagnosticInterface
	fixtures := []struct {
		name, address, class string
		flags                net.Flags
	}{
		{"ens-fixture", "192.0.2.1", "physical_default_route", net.FlagUp},
		{"eth-other", "192.0.2.2", "other_physical", net.FlagUp},
		{"docker0", "192.0.2.3", "docker_bridge", net.FlagUp},
		{"br-fixture", "192.0.2.4", "docker_bridge", net.FlagUp},
		{"veth-fixture", "192.0.2.5", "veth", net.FlagUp},
		{"tailscale0", "192.0.2.6", "tailscale", net.FlagUp},
		{"lo", "127.0.0.1", "loopback", net.FlagUp | net.FlagLoopback},
		{"eth-ipv6", "2001:db8::1", "other_physical", net.FlagUp},
		{"", "192.0.2.7", "unknown", 0},
	}
	for _, f := range fixtures {
		table = append(table, diagnosticInterface{f.name, f.flags, []netip.Addr{netip.MustParseAddr(f.address)}})
	}
	classes := diagnosticInterfaceClasses(table, "ens-fixture")
	for _, f := range fixtures {
		if got := interfaceClassOf(classes, f.address); got != f.class {
			t.Errorf("class=%s want=%s", got, f.class)
		}
	}
	for address, want := range map[string]string{"192.0.2.99": "unknown", "private.invalid": "unknown", "::ffff:192.0.2.1": "physical_default_route", "2001:db8::1%eth-ipv6": "other_physical"} {
		if got := interfaceClassOf(classes, address); got != want {
			t.Errorf("class=%s want=%s", got, want)
		}
	}
	if got := diagnosticInterfaceClasses(table, "docker0")["192.0.2.3"]; got != "docker_bridge" {
		t.Fatal("virtual default incorrectly labeled physical")
	}
	table = append(table, diagnosticInterface{"veth-duplicate", net.FlagUp, []netip.Addr{netip.MustParseAddr("192.0.2.1")}})
	if got := diagnosticInterfaceClasses(table, "ens-fixture")["192.0.2.1"]; got != "unknown" {
		t.Fatal("ambiguous interface not unknown")
	}
}

func TestDefaultIPv4Route(t *testing.T) {
	row := func(name, dest, flags, metric, mask string) string {
		return strings.Join([]string{name, dest, "010200C0", flags, "0", "0", metric, mask, "0", "0", "0"}, "\t") + "\n"
	}
	defaultRow := row("ens-fixture", "00000000", "0003", "100", "00000000")
	for _, tc := range []struct{ table, want string }{
		{defaultRow, "ens-fixture"},
		{"Iface Destination Gateway Flags RefCnt Use Metric Mask\n" + defaultRow, "ens-fixture"},
		{defaultRow + row("other", "00000000", "0003", "200", "00000000"), "ens-fixture"},
		{row("other", "00000000", "0003", "200", "00000000") + defaultRow, "ens-fixture"},
		{defaultRow + row("other", "00000000", "0003", "100", "00000000"), ""},
		{defaultRow + defaultRow, "ens-fixture"},
		{row("other", "00000000", "0203", "0", "00000000"), ""},
		{row("other", "00000000", "0002", "0", "00000000"), ""},
		{row("other", "00000000", "0003", "bad", "00000000"), ""},
		{row("other", "00000000", "badflags", "0", "00000000"), ""},
		{row("other", "010200C0", "0003", "0", "00FFFFFF"), ""},
		{"", ""}, {"malformed", ""},
	} {
		got, err := defaultIPv4RouteInterface(tc.table)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Fatalf("got=%q want=%q err=%v", got, tc.want, err)
		}
	}
}

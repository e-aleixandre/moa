package voice

import (
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// DefaultIPv4RouteInterface reads the local routing table, without network I/O.
// Ambiguous equal-metric defaults are rejected rather than guessing a device.
func DefaultIPv4RouteInterface() (string, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return "", errors.New("default IPv4 route unavailable")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil || len(b) == 1<<20 {
		return "", errors.New("default IPv4 route unavailable")
	}
	name, err := defaultIPv4RouteInterface(string(b))
	if err != nil {
		return "", err
	}
	iface, err := net.InterfaceByName(name)
	if err != nil || iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
		return "", errors.New("default IPv4 interface unavailable")
	}
	return name, nil
}

func defaultIPv4RouteInterface(table string) (string, error) {
	name := ""
	var best uint64
	ambiguous := false
	for _, line := range strings.Split(table, "\n") {
		f := strings.Fields(line)
		if len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		flags, e1 := strconv.ParseUint(f[3], 16, 32)
		metric, e2 := strconv.ParseUint(f[6], 10, 64)
		if e1 != nil || e2 != nil || flags&1 == 0 || flags&0x200 != 0 {
			continue
		}
		if name == "" || metric < best {
			name, best, ambiguous = f[0], metric, false
		} else if metric == best && name != f[0] {
			ambiguous = true
		}
	}
	if name == "" || ambiguous {
		return "", errors.New("default IPv4 route unavailable or ambiguous")
	}
	return name, nil
}

type diagnosticInterface struct {
	name      string
	flags     net.Flags
	addresses []netip.Addr
}

func diagnosticInterfaceClasses(interfaces []diagnosticInterface, defaultRoute string) map[string]string {
	out := make(map[string]string)
	for _, iface := range interfaces {
		class := "other_physical"
		switch {
		case iface.flags&net.FlagLoopback != 0:
			class = "loopback"
		case iface.name == "docker0" || strings.HasPrefix(iface.name, "br-"):
			class = "docker_bridge"
		case strings.HasPrefix(iface.name, "veth"):
			class = "veth"
		case strings.HasPrefix(iface.name, "tailscale"):
			class = "tailscale"
		case iface.name != "" && iface.name == defaultRoute:
			class = "physical_default_route"
		case iface.name == "":
			class = "unknown"
		}
		for _, address := range iface.addresses {
			key := address.Unmap().WithZone("").String()
			if old, ok := out[key]; ok && old != class {
				out[key] = "unknown"
			} else {
				out[key] = class
			}
		}
	}
	return out
}

// The immutable address-to-enum table is built before callbacks are installed.
func localDiagnosticInterfaceClasses() map[string]string {
	defaultRoute, _ := DefaultIPv4RouteInterface()
	interfaces, _ := net.Interfaces()
	var table []diagnosticInterface
	for _, iface := range interfaces {
		row := diagnosticInterface{name: iface.Name, flags: iface.Flags}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			if prefix, err := netip.ParsePrefix(address.String()); err == nil {
				row.addresses = append(row.addresses, prefix.Addr())
			}
		}
		table = append(table, row)
	}
	return diagnosticInterfaceClasses(table, defaultRoute)
}

package mdns

import (
	"net"
	"os"
)

// osHostname wraps os.Hostname so it can be referenced from mdns.go without an
// extra import there.
func osHostname() (string, error) {
	return os.Hostname()
}

// localIPv4s returns the non-loopback, non-link-local IPv4 addresses bound to
// up, non-loopback interfaces. These populate the advertised A records.
func localIPv4s() []net.IP {
	var ips []net.IP
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			ip4 := ip.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			ips = append(ips, ip4)
		}
	}
	return ips
}

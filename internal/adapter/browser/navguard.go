package browser

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// IPLookupFunc resolves hostnames to IP addresses (replaceable in unit tests).
type IPLookupFunc func(ctx context.Context, host string) ([]net.IP, error)

func defaultLookupIP(ctx context.Context, host string) ([]net.IP, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(cctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

// AllowedURL checks scheme, hostname, and DNS resolution against SSRF private/loopback/metadata ranges (Spec 8.10).
func AllowedURL(raw string) (bool, string) {
	return AllowedURLWithLookup(context.Background(), raw, defaultLookupIP)
}

// AllowedURLWithLookup validates raw using a custom DNS resolver.
func AllowedURLWithLookup(ctx context.Context, raw string, lookup IPLookupFunc) (bool, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false, "empty URL"
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return false, "malformed URL"
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return false, "chỉ cho phép giao thức http hoặc https"
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return false, "thiếu tên miền"
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return false, "cấm truy cập tên miền nội bộ/loopback"
	}

	if ip, err := netip.ParseAddr(host); err == nil {
		if isDisallowedAddr(ip) {
			return false, "cấm truy cập địa chỉ IP nội bộ/loopback/metadata"
		}
		return true, ""
	}

	if lookup == nil {
		lookup = defaultLookupIP
	}
	ips, err := lookup(ctx, host)
	if err != nil || len(ips) == 0 {
		return false, "không phân giải được tên miền"
	}
	for _, stdIP := range ips {
		addr, ok := netip.AddrFromSlice(stdIP)
		if !ok || isDisallowedAddr(addr) {
			return false, "tên miền phân giải ra địa chỉ IP nội bộ/loopback"
		}
	}
	return true, ""
}

func isDisallowedAddr(addr netip.Addr) bool {
	u := addr.Unmap()
	if !u.IsValid() {
		return true
	}
	if u.IsLoopback() || u.IsPrivate() || u.IsLinkLocalUnicast() || u.IsLinkLocalMulticast() || u.IsMulticast() || u.IsUnspecified() {
		return true
	}
	// Block CGNAT / Tailscale 100.64.0.0/10 and cloud metadata 169.254.0.0/16 explicitly.
	if u.Is4() {
		b := u.As4()
		if b[0] == 100 && b[1] >= 64 && b[1] <= 127 {
			return true
		}
		if b[0] == 169 && b[1] == 254 {
			return true
		}
		if b[0] == 0 {
			return true
		}
	}
	return false
}

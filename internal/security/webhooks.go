package security

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

func PublicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/32", "2002::/16", "64:ff9b::/96", "64:ff9b:1::/48", "192.88.99.0/24"} {
		if netip.MustParsePrefix(p).Contains(a) {
			return false
		}
	}
	return true
}
func ValidateWebhook(raw string, allowPrivate bool) error {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || len(raw) > 2048 {
		return fmt.Errorf("invalid webhook URL")
	}
	if u.Scheme != "https" && !(allowPrivate && u.Scheme == "http") {
		return fmt.Errorf("webhook requires HTTPS")
	}
	if strings.EqualFold(u.Hostname(), "localhost") && !allowPrivate {
		return fmt.Errorf("private webhook prohibited")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !allowPrivate && !PublicIP(ip) {
		return fmt.Errorf("private webhook prohibited")
	}
	return nil
}
func WebhookClient(allowPrivate bool) *http.Client {
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 32 * 1024, DisableKeepAlives: true}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, e
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("empty DNS response")
		}
		for _, ip := range ips {
			if !allowPrivate && !PublicIP(ip.IP) {
				return nil, fmt.Errorf("webhook DNS resolves to a private or reserved address")
			}
		}
		// Connect to the IP actually validated, preventing DNS rebinding between checks.
		var last error
		for _, ip := range ips {
			conn, e := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if e == nil {
				return conn, nil
			}
			last = e
		}
		return nil, last
	}
	return &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

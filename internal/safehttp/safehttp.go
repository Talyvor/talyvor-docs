// Package safehttp provides an HTTP client hardened against SSRF. Its dialer resolves the target host
// and refuses to connect to loopback, private (RFC1918 / ULA), link-local (169.254/16 incl. the cloud
// metadata endpoint 169.254.169.254, fe80::/10), unspecified, and multicast addresses — and it
// re-validates every redirect hop (each redirect re-dials through the same guard). Use it for every
// fetch of a user-supplied or fetched-then-followed URL (webhooks).
package safehttp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// ErrBlockedAddress is returned when a fetch targets a non-public (SSRF-sensitive) address.
var ErrBlockedAddress = errors.New("safehttp: refusing to connect to a non-public address")

// blocked reports whether ip is in an SSRF-sensitive range that must never be dialed.
func blocked(ip net.IP) bool {
	return ip.IsLoopback() || // 127.0.0.0/8, ::1
		ip.IsPrivate() || // 10/8, 172.16/12, 192.168/16, fc00::/7 (ULA)
		ip.IsLinkLocalUnicast() || // 169.254.0.0/16 (incl. metadata 169.254.169.254), fe80::/10
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsUnspecified() || // 0.0.0.0, ::
		ip.IsMulticast()
}

// resolve looks host up and refuses it when any address it resolves to is in a blocked range.
func resolve(ctx context.Context, host string) ([]net.IP, error) {
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("safehttp: no addresses for %q", host)
	}
	for _, ip := range ips {
		if blocked(ip) {
			return nil, fmt.Errorf("%w: %s resolves to %s", ErrBlockedAddress, host, ip)
		}
	}
	return ips, nil
}

// Check refuses a host the client would refuse to dial, so a URL can be rejected when it is
// registered. The dial still re-checks: a name can resolve differently later.
func Check(ctx context.Context, host string) error {
	_, err := resolve(ctx, host)
	return err
}

// safeDialContext resolves the host, rejects any resolved address in a blocked range, and dials the
// resolved IP directly (so a DNS name can't rebind to an internal IP between the check and the connect).
func safeDialContext(base *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := resolve(ctx, host)
		if err != nil {
			return nil, err
		}
		// Dial the already-validated address, not the hostname (defeats DNS rebinding).
		return base.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
}

// Client returns an *http.Client with the SSRF dial guard installed on both the initial request and
// every redirect hop.
func Client(timeout time.Duration) *http.Client {
	base := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           safeDialContext(base),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("safehttp: stopped after 10 redirects")
			}
			return nil // each redirect re-dials through safeDialContext, which re-validates the target
		},
	}
}

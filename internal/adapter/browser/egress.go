package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// EgressProxy enforces dial-time SSRF IP filtering for all browser HTTP and HTTPS CONNECT traffic (S1 Mandatory).
type EgressProxy struct {
	ln  net.Listener
	srv *http.Server
	url string
}

// StartEgressProxy binds a loopback HTTP/CONNECT proxy that resolves DNS at dial time and blocks private/loopback IPs.
func StartEgressProxy(ctx context.Context, lookup IPLookupFunc) (*EgressProxy, error) {
	if lookup == nil {
		lookup = defaultLookupIP
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen egress proxy: %w", err)
	}

	safeDial := func(dctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if host == "localhost" {
			return nil, errors.New("egress blocked: localhost")
		}
		if parsed, pErr := netip.ParseAddr(host); pErr == nil {
			if isDisallowedAddr(parsed) {
				return nil, fmt.Errorf("egress blocked IP %s", parsed)
			}
			var d net.Dialer
			return d.DialContext(dctx, network, net.JoinHostPort(parsed.Unmap().String(), port))
		}
		ips, err := lookup(dctx, host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("egress lookup failed for %q", host)
		}
		var safeIP string
		for _, ip := range ips {
			addr, ok := netip.AddrFromSlice(ip)
			if !ok || isDisallowedAddr(addr) {
				return nil, fmt.Errorf("egress blocked resolved IP %v for %q", ip, host)
			}
			if safeIP == "" {
				safeIP = addr.Unmap().String()
			}
		}
		var d net.Dialer
		return d.DialContext(dctx, network, net.JoinHostPort(safeIP, port))
	}

	transport := &http.Transport{
		DialContext:           safeDial,
		ResponseHeaderTimeout: 30 * time.Second,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			upstream, err := safeDial(r.Context(), "tcp", r.Host)
			if err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
			hj, ok := w.(http.Hijacker)
			if !ok {
				_ = upstream.Close()
				http.Error(w, "hijack unsupported", http.StatusInternalServerError)
				return
			}
			clientConn, _, err := hj.Hijack()
			if err != nil {
				_ = upstream.Close()
				return
			}
			_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
			go func() {
				defer func() { _ = upstream.Close(); _ = clientConn.Close() }()
				_, _ = io.Copy(upstream, clientConn)
			}()
			go func() {
				defer func() { _ = clientConn.Close(); _ = upstream.Close() }()
				_, _ = io.Copy(clientConn, upstream)
			}()
			return
		}

		if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
			http.Error(w, "unsupported scheme", http.StatusForbidden)
			return
		}
		outReq := r.Clone(r.Context())
		outReq.RequestURI = ""
		resp, err := transport.RoundTrip(outReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		for k, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	p := &EgressProxy{
		ln:  ln,
		srv: srv,
		url: "http://" + ln.Addr().String(),
	}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		p.Close()
	}()
	return p, nil
}

// URL returns the http://127.0.0.1:<port> proxy URL.
func (p *EgressProxy) URL() string { return p.url }

// Close shuts down the egress proxy listener.
func (p *EgressProxy) Close() {
	if p != nil && p.srv != nil {
		_ = p.srv.Close()
	}
}

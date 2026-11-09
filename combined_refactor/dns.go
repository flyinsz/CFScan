package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"
)

func initCustomResolver() {
	servers := normalizeDNSServers(customDNSServer)
	if len(servers) == 0 {
		customResolver = nil
		return
	}
	customResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			var errs []error
			for _, server := range servers {
				dialer := net.Dialer{Timeout: 5 * time.Second}
				conn, err := dialer.DialContext(ctx, network, server)
				if err == nil || network == "tcp" {
					return conn, err
				}
				errs = append(errs, err)
				conn, err = dialer.DialContext(ctx, "tcp", server)
				if err == nil {
					return conn, nil
				}
				errs = append(errs, err)
			}
			return nil, errors.Join(errs...)
		},
	}
}

func normalizeDNSServers(value string) []string {
	fields := strings.FieldsFunc(strings.TrimSpace(value), func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})
	servers := make([]string, 0, len(fields))
	for _, field := range fields {
		server := strings.TrimSpace(field)
		if server == "" {
			continue
		}
		if host, port, err := net.SplitHostPort(server); err == nil {
			if host == "" || port == "" {
				server = net.JoinHostPort(strings.Trim(host, "[]"), "53")
			}
		} else if ip := net.ParseIP(strings.Trim(server, "[]")); ip != nil {
			server = net.JoinHostPort(ip.String(), "53")
		} else {
			server = net.JoinHostPort(server, "53")
		}
		servers = append(servers, server)
	}
	return servers
}

func dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialContextWithTimeout(ctx, network, addr, 30*time.Second)
}

func dialContextWithTimeout(ctx context.Context, network, addr string, timeout time.Duration) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 30 * time.Second}
	if timeout > 0 {
		dialer.Timeout = timeout
	}
	if customResolver != nil && customDNSForced {
		dialer.Resolver = customResolver
		return dialer.DialContext(ctx, network, addr)
	}
	conn, err := dialer.DialContext(ctx, network, addr)
	if err == nil || customResolver == nil || !isDNSError(err) {
		return conn, err
	}
	dialer.Resolver = customResolver
	return dialer.DialContext(ctx, network, addr)
}

func isDNSError(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

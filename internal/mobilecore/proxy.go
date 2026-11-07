package mobilecore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"strings"

	"motty/internal/sshconn"
)

// The vault name of a host proxy's password. It is the desktop's name
// (internal/workbench/outbound_proxy.go), so a password saved in either version
// reaches the other through a backup or a sync.
func hostProxyPasswordSecret(catalogID string, proxy *HostProxy) string {
	if catalogID == "" || proxy == nil || proxy.User == "" {
		return ""
	}
	login := sha256.Sum256([]byte(proxy.Kind + "\x00" + proxy.Address + "\x00" + proxy.User))
	return fmt.Sprintf("host-proxy:%s:%x:password", catalogID, login[:8])
}

// proxyConfig turns a saved proxy into what sshconn dials through, with the
// password read from the vault. Nil means a direct connection.
func hostProxyFor(data *dataStore, host Host) (*sshconn.Proxy, error) {
	if host.Proxy == nil {
		return nil, nil
	}
	kind := sshconn.ProxyNone
	switch host.Proxy.Kind {
	case ProxySOCKS5:
		kind = sshconn.ProxySOCKS5
	case ProxyHTTP:
		kind = sshconn.ProxyHTTPConnect
	}
	if kind == sshconn.ProxyNone {
		return nil, nil
	}
	password := ""
	if name := hostProxyPasswordSecret(host.ID, host.Proxy); name != "" && data != nil {
		value, found, err := data.secretGet(name)
		if err != nil {
			return nil, err
		}
		if found {
			password = value
		}
	}
	return &sshconn.Proxy{
		Kind:     kind,
		Address:  host.Proxy.Address,
		Username: host.Proxy.User,
		Password: password,
		Bypass:   splitProxyBypass(host.Proxy.Bypass),
	}, nil
}

// splitProxyBypass reads the bypass list the way the desktop does: entries
// separated by commas or spaces.
func splitProxyBypass(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	entries := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			entries = append(entries, field)
		}
	}
	return entries
}

// proxyDialer is the desktop.Dialer a remote desktop session uses when its
// host leaves through a proxy. Destinations on the proxy's bypass list go
// direct.
func proxyDialer(proxy *sshconn.Proxy) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if !proxy.Uses(address) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, address)
		}
		return proxy.Dial(ctx, dialTimeout, address)
	}
}

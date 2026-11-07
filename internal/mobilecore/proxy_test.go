package mobilecore

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"motty/internal/sshconn"
)

func TestHostProxyIsTrimmedAndValidated(t *testing.T) {
	store, err := openHostStore(openTestData(t))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.save(Host{
		Label: "desk", Address: "example.com", Protocol: ProtocolRDP, User: "me",
		Proxy: &HostProxy{Kind: " SOCKS5 ", Address: " proxy.example.com:1080 ", User: " pu ", Bypass: " localhost , 10.0.0.0/8 "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Proxy == nil || saved.Proxy.Kind != ProxySOCKS5 ||
		saved.Proxy.Address != "proxy.example.com:1080" || saved.Proxy.User != "pu" ||
		saved.Proxy.Bypass != "localhost , 10.0.0.0/8" {
		t.Fatalf("saved proxy = %+v", saved.Proxy)
	}
	// The proxy is part of the record that syncs and backs up, without a
	// password.
	got, ok := store.get(saved.ID)
	if !ok || got.Proxy == nil || got.Proxy.Address != "proxy.example.com:1080" {
		t.Fatalf("stored host = %+v, %v", got.Proxy, ok)
	}

	if err := (Host{Label: "x", Address: "a", User: "me", Proxy: &HostProxy{Kind: ProxySOCKS5}}).Validate(); err == nil {
		t.Fatal("a proxy without an address must be rejected")
	}
	if err := (Host{Label: "x", Address: "a", User: "me", Proxy: &HostProxy{Kind: "ftp", Address: "p:1"}}).Validate(); err == nil {
		t.Fatal("an unknown proxy kind must be rejected")
	}
	if p := (Host{Proxy: &HostProxy{Kind: "ftp", Address: "p:1"}}).Normalized().Proxy; p != nil {
		t.Fatalf("an unknown proxy kind should be dropped, got %+v", p)
	}
}

func TestHostProxyPasswordIsNamedLikeTheDesktop(t *testing.T) {
	proxy := &HostProxy{Kind: ProxySOCKS5, Address: "proxy.example.com:1080", User: "pu"}
	login := sha256.Sum256([]byte("socks5\x00proxy.example.com:1080\x00pu"))
	want := fmt.Sprintf("host-proxy:web:%x:password", login[:8])
	if got := hostProxyPasswordSecret("web", proxy); got != want {
		t.Fatalf("secret name = %q, want %q", got, want)
	}
	if name := hostProxyPasswordSecret("web", &HostProxy{Kind: ProxySOCKS5, Address: "p:1"}); name != "" {
		t.Fatalf("a proxy without a login must not have a password: %q", name)
	}
}

func TestHostProxyPasswordRPCStoresItUnderTheProxyLogin(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	envelope := callApp(t, app, "hosts.save",
		`{"label":"desk","address":"example.com","protocol":"rdp","user":"me","proxy":{"kind":"http","address":"proxy.example.com:3128","user":"pu"}}`)
	if !envelope.OK {
		t.Fatalf("hosts.save failed: %+v", envelope.Error)
	}
	var host Host
	if err := json.Unmarshal(envelope.Result, &host); err != nil {
		t.Fatal(err)
	}

	set := callApp(t, app, "hosts.password",
		`{"id":`+quoteJSON(host.ID)+`,"kind":"proxy","password":"proxy-secret"}`)
	if !set.OK {
		t.Fatalf("hosts.password failed: %+v", set.Error)
	}
	name := hostProxyPasswordSecret(host.ID, host.Proxy)
	if value, found, err := app.data.secretGet(name); err != nil || !found || value != "proxy-secret" {
		t.Fatalf("vault proxy password = %q, %v, %v", value, found, err)
	}

	status := callApp(t, app, "hosts.passwordStatus", `{"id":`+quoteJSON(host.ID)+`,"kind":"proxy"}`)
	var proxyStatus struct {
		Saved bool `json:"saved"`
	}
	if err := json.Unmarshal(status.Result, &proxyStatus); err != nil || !proxyStatus.Saved {
		t.Fatalf("proxy status = %s", status.Result)
	}
	// The login password is a different entry, so it stays unset.
	login := callApp(t, app, "hosts.passwordStatus", `{"id":`+quoteJSON(host.ID)+`}`)
	var loginStatus struct {
		Saved bool `json:"saved"`
	}
	if err := json.Unmarshal(login.Result, &loginStatus); err != nil || loginStatus.Saved {
		t.Fatalf("login status = %s", login.Result)
	}

	remove := callApp(t, app, "hosts.password", `{"id":`+quoteJSON(host.ID)+`,"kind":"proxy","remove":true}`)
	if !remove.OK {
		t.Fatalf("removing the proxy password failed: %+v", remove.Error)
	}
	if _, found, _ := app.data.secretGet(name); found {
		t.Fatal("the proxy password survived its removal")
	}
}

func TestHostProxyForReadsTheVaultAndKeepsTheBypass(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	host := Host{
		ID: "host_1",
		Proxy: &HostProxy{
			Kind: ProxyHTTP, Address: "proxy.example.com:3128", User: "pu",
			Bypass: "localhost, 127.0.0.0/8  *.corp",
		},
	}
	if err := app.data.secretSet(hostProxyPasswordSecret(host.ID, host.Proxy), "proxy-secret"); err != nil {
		t.Fatal(err)
	}
	proxy, err := hostProxyFor(app.data, host)
	if err != nil {
		t.Fatal(err)
	}
	if proxy == nil || proxy.Kind != sshconn.ProxyHTTPConnect || proxy.Address != "proxy.example.com:3128" ||
		proxy.Username != "pu" || proxy.Password != "proxy-secret" {
		t.Fatalf("proxy = %+v", proxy)
	}
	if len(proxy.Bypass) != 3 || proxy.Bypass[0] != "localhost" || proxy.Bypass[2] != "*.corp" {
		t.Fatalf("bypass = %q", proxy.Bypass)
	}
	if !proxy.Configured() {
		t.Fatal("a complete proxy is not configured")
	}

	// A host without a proxy dials directly.
	if direct, err := hostProxyFor(app.data, Host{ID: "host_2"}); err != nil || direct != nil {
		t.Fatalf("direct host proxy = %+v, %v", direct, err)
	}
}

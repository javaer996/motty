package privacy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func replacementOf(t *testing.T, replacements []Replacement, category Category, original string) Replacement {
	t.Helper()
	for _, replacement := range replacements {
		if replacement.Category == category && replacement.Original == original {
			return replacement
		}
	}
	t.Fatalf("no %s replacement for %q in %+v", category, original, replacements)
	return Replacement{}
}

func assertNoSecret(t *testing.T, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(text, secret) {
			t.Errorf("%q still present in %q", secret, text)
		}
	}
}

func TestKnownValuesRespectBoundaries(t *testing.T) {
	guard := NewGuard(Config{
		KnownValues: true,
		Known:       []Known{{Value: "alice", Category: CategoryUser, Boundary: true}},
	})
	out, replacements := guard.Protect("alice alice2 alice@host")
	want := "⟦USER_1⟧ alice2 ⟦USER_1⟧@host"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
	if len(replacements) != 1 || replacements[0].Original != "alice" {
		t.Fatalf("unexpected replacements: %+v", replacements)
	}
}

func TestCustomStringsReplaceLongestFirst(t *testing.T) {
	guard := NewGuard(Config{
		Custom:        true,
		CustomStrings: []string{"alice", "alice@example.com"},
	})
	out, replacements := guard.Protect("alice@example.com and alice")
	email := replacementOf(t, replacements, CategoryCustom, "alice@example.com")
	name := replacementOf(t, replacements, CategoryCustom, "alice")
	want := email.Token + " and " + name.Token
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestAddresses(t *testing.T) {
	guard := NewGuard(Config{IPs: true})
	input := "ssh 192.168.1.20 [2001:db8::5]:22 127.0.0.1 0.0.0.0 999.1.2.3 v1.2.3.4 aa:bb:cc:dd:ee:ff fe80::1%en0"
	out, replacements := guard.Protect(input)
	v4 := replacementOf(t, replacements, CategoryIP, "192.168.1.20")
	v6 := replacementOf(t, replacements, CategoryIP, "2001:db8::5")
	zone := replacementOf(t, replacements, CategoryIP, "fe80::1%en0")
	want := "ssh " + v4.Token + " [" + v6.Token + "]:22 127.0.0.1 0.0.0.0 999.1.2.3 v1.2.3.4 aa:bb:cc:dd:ee:ff " + zone.Token
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestAccountsAndHomePaths(t *testing.T) {
	guard := NewGuard(Config{Accounts: true, IPs: true})
	input := "/home/alice/.ssh/id_rsa /Users/bob alice@server bob@10.0.0.7 root@server www-data@server sudo -u charlie su - dave"
	out, replacements := guard.Protect(input)
	alice := replacementOf(t, replacements, CategoryUser, "alice")
	bob := replacementOf(t, replacements, CategoryUser, "bob")
	charlie := replacementOf(t, replacements, CategoryUser, "charlie")
	dave := replacementOf(t, replacements, CategoryUser, "dave")
	address := replacementOf(t, replacements, CategoryIP, "10.0.0.7")
	want := "/home/" + alice.Token + "/.ssh/id_rsa /Users/" + bob.Token +
		" " + alice.Token + "@server " + bob.Token + "@" + address.Token +
		" root@server www-data@server sudo -u " + charlie.Token + " su - " + dave.Token
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestHostsAndEmailsAreOptional(t *testing.T) {
	guard := NewGuard(Config{Accounts: true, Hosts: true, Emails: true, IPs: true})
	out, replacements := guard.Protect("alice@example.com bob@server api.example.com")
	email := replacementOf(t, replacements, CategoryEmail, "alice@example.com")
	user := replacementOf(t, replacements, CategoryUser, "bob")
	host := replacementOf(t, replacements, CategoryHost, "server")
	domain := replacementOf(t, replacements, CategoryHost, "api.example.com")
	want := email.Token + " " + user.Token + "@" + host.Token + " " + domain.Token
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestHostsStayVisibleByDefault(t *testing.T) {
	guard := NewGuard(Config{Accounts: true})
	out, _ := guard.Protect("alice@example.com")
	if out != "⟦USER_1⟧@example.com" {
		t.Fatalf("got %q", out)
	}
}

func TestSystemAccountsAreNotTokens(t *testing.T) {
	guard := NewGuard(Config{Accounts: true})
	input := "Root@server www-data@server /home/root /Users/ubuntu su root"
	out, _ := guard.Protect(input)
	if out != input {
		t.Fatalf("system accounts changed: %q", out)
	}
}

func TestSecrets(t *testing.T) {
	guard := NewGuard(Config{Secrets: true})
	input := `password="s3cret value" api_key: abc123 --token=hunter2 ` +
		`Authorization: Bearer abcdefgh12345678 postgres://alice:s3cretpass@db.internal:5432/app`
	out, replacements := guard.Protect(input)
	assertNoSecret(t, out, "s3cret value", "abc123", "hunter2", "abcdefgh12345678", "alice", "s3cretpass")
	for _, original := range []string{"s3cret value", "abc123", "hunter2", "abcdefgh12345678", "s3cretpass"} {
		replacementOf(t, replacements, CategorySecret, original)
	}
	replacementOf(t, replacements, CategoryUser, "alice")
	if !strings.Contains(out, "@db.internal:5432/app") {
		t.Fatalf("host or path was damaged: %q", out)
	}
}

func TestVendorTokens(t *testing.T) {
	guard := NewGuard(Config{Secrets: true})
	input := "AKIAIOSFODNN7EXAMPLE eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefgh ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	out, replacements := guard.Protect(input)
	assertNoSecret(t, out, "AKIAIOSFODNN7EXAMPLE", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefgh", "ghp_abcdefghijklmnopqrstuvwxyz0123456789")
	for _, original := range []string{"AKIAIOSFODNN7EXAMPLE", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefgh", "ghp_abcdefghijklmnopqrstuvwxyz0123456789"} {
		replacementOf(t, replacements, CategorySecret, original)
	}
}

func TestPEMBlocksAndFlatKeys(t *testing.T) {
	guard := NewGuard(Config{Secrets: true})
	block := "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\nBBBB\n-----END OPENSSH PRIVATE KEY-----"
	input := "key:\n" + block + "\nnext"
	out, replacements := guard.Protect(input)
	key := replacementOf(t, replacements, CategoryKey, block)
	if want := "key:\n" + key.Token + "\nnext"; out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
	restored, _ := guard.Restore(out)
	if restored != input {
		t.Fatalf("restored %q, want %q", restored, input)
	}

	certificate := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"
	certOut, certReplacements := guard.Protect(certificate)
	cert := replacementOf(t, certReplacements, CategoryCert, certificate)
	if certOut != cert.Token {
		t.Fatalf("certificate got %q", certOut)
	}

	flat := "-----BEGIN RSA PRIVATE KEY-----MIIB-----END RSA PRIVATE KEY-----"
	flatOut, flatReplacements := guard.Protect("before " + flat + " after")
	flatKey := replacementOf(t, flatReplacements, CategoryKey, flat)
	if want := "before " + flatKey.Token + " after"; flatOut != want {
		t.Fatalf("flat key got %q, want %q", flatOut, want)
	}
}

func TestSSHPublicKeyBlob(t *testing.T) {
	guard := NewGuard(Config{Secrets: true})
	blob := "AAAAC3NzaC1lZDI1NTE5AAAAIKq7c7r5F1o0mJ6gD9nM2sT8vQ4xY6bB8cD0eF2gH4iJ"
	input := "ssh-ed25519 " + blob + " alice@laptop"
	out, replacements := guard.Protect(input)
	key := replacementOf(t, replacements, CategoryKey, blob)
	if want := "ssh-ed25519 " + key.Token + " alice@laptop"; out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestTokenIsNotTokenizedAgain(t *testing.T) {
	guard := NewGuard(Config{
		KnownValues: true,
		Secrets:     true,
		Known:       []Known{{Value: "hunter2", Category: CategorySecret}},
	})
	out, replacements := guard.Protect("password=hunter2")
	secret := replacementOf(t, replacements, CategorySecret, "hunter2")
	if want := "password=" + secret.Token; out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
	if len(replacements) != 1 {
		t.Fatalf("value tokenized twice: %+v", replacements)
	}
	restored, _ := guard.Restore(out)
	if restored != "password=hunter2" {
		t.Fatalf("restore got %q", restored)
	}
}

func TestTokensAreStableWithinOneGuard(t *testing.T) {
	guard := NewGuard(Config{IPs: true})
	_, first := guard.Protect("192.168.1.20")
	_, second := guard.Protect("again 192.168.1.20")
	if first[0].Token != second[0].Token {
		t.Fatalf("token changed between messages: %q vs %q", first[0].Token, second[0].Token)
	}
	fresh := NewGuard(Config{IPs: true})
	_, other := fresh.Protect("192.168.1.20")
	if other[0].Token != "⟦IP_1⟧" {
		t.Fatalf("fresh guard token %q", other[0].Token)
	}
}

func TestUserAtHostKeepsProtectingHostAfterKnownUser(t *testing.T) {
	guard := NewGuard(Config{
		KnownValues: true,
		Accounts:    true,
		Hosts:       true,
		Known:       []Known{{Value: "alice", Category: CategoryUser, Boundary: true}},
	})
	out, replacements := guard.Protect("alice@server")
	user := replacementOf(t, replacements, CategoryUser, "alice")
	host := replacementOf(t, replacements, CategoryHost, "server")
	if want := user.Token + "@" + host.Token; out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestRestoreRoundTrip(t *testing.T) {
	guard := NewGuard(Config{
		KnownValues: true,
		Secrets:     true,
		IPs:         true,
		Accounts:    true,
		Emails:      true,
		Hosts:       true,
		Known:       []Known{{Value: "hunter2", Category: CategorySecret, Boundary: true}},
	})
	input := "alice@example.com ssh 192.168.1.20 with token=hunter2 home /Users/alice " +
		"prompt alice@example.com and password=hunter2"
	protected, replacements := guard.Protect(input)
	if protected == input {
		t.Fatal("nothing protected")
	}
	if len(replacements) == 0 {
		t.Fatal("no replacements reported")
	}
	restored, restoredReplacements := guard.Restore(protected)
	if restored != input {
		t.Fatalf("restored %q, want %q", restored, input)
	}
	if len(restoredReplacements) == 0 {
		t.Fatal("no inbound replacements reported")
	}
}

func TestStreamRestorerSurvivesChunkBoundaries(t *testing.T) {
	guard := NewGuard(Config{
		Secrets:  true,
		IPs:      true,
		Accounts: true,
	})
	input := "alice@192.168.1.20 ran password=hunter2 and /home/alice/x"
	protected, _ := guard.Protect(input)
	if strings.Contains(protected, "192.168.1.20") {
		t.Fatalf("address not protected: %q", protected)
	}
	for _, size := range []int{1, 2, 3, 5, 7, 64} {
		restorer := guard.NewStreamRestorer()
		var out strings.Builder
		for start := 0; start < len(protected); start += size {
			end := start + size
			if end > len(protected) {
				end = len(protected)
			}
			out.WriteString(restorer.Write(protected[start:end]))
		}
		out.WriteString(restorer.Flush())
		if out.String() != input {
			t.Fatalf("chunk size %d: got %q, want %q", size, out.String(), input)
		}
		if len(restorer.Replacements()) == 0 {
			t.Fatalf("chunk size %d: no restorations recorded", size)
		}
	}
}

func TestUnknownTokenStaysLiteral(t *testing.T) {
	guard := NewGuard(Config{IPs: true})
	guard.Protect("192.168.1.20")
	out, _ := guard.Restore("a ⟦EMAIL_9⟧ b")
	if out != "a ⟦EMAIL_9⟧ b" {
		t.Fatalf("unknown token changed: %q", out)
	}
}

func TestProtectAndRestoreJSON(t *testing.T) {
	guard := NewGuard(Config{
		KnownValues: true,
		Secrets:     true,
		IPs:         true,
		Known:       []Known{{Value: `pa"ss`, Category: CategorySecret}},
	})
	raw := `{"command":"ssh alice@192.168.1.20","note":"pa\"ss"}`
	protected, replacements := guard.ProtectJSON(raw)
	assertNoSecret(t, protected, "192.168.1.20", `pa"ss`)
	replacementOf(t, replacements, CategoryIP, "192.168.1.20")
	if !json.Valid([]byte(protected)) {
		t.Fatalf("protected JSON is invalid: %q", protected)
	}
	restored, _ := guard.RestoreJSON(protected)
	var want, got any
	if err := json.Unmarshal([]byte(raw), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(restored), &got); err != nil {
		t.Fatalf("restored JSON is invalid: %q (%v)", restored, err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("JSON round trip changed the document: %#v vs %#v", want, got)
	}
}

func TestMaskAndPreview(t *testing.T) {
	cases := []struct {
		category Category
		value    string
		want     string
	}{
		{CategorySecret, "hunter2", "hu••••r2"},
		{CategoryKey, "abcd", "••••"},
		{CategoryCert, "", ""},
		{CategoryIP, "192.168.1.20", "192.168.1.20"},
		{CategoryUser, "alice", "alice"},
	}
	for _, item := range cases {
		if got := Mask(item.category, item.value); got != item.want {
			t.Errorf("Mask(%s, %q) = %q, want %q", item.category, item.value, got, item.want)
		}
	}
	long := strings.Repeat("中", 200)
	preview := Preview(long, 160)
	if len([]rune(preview)) != 161 || !strings.HasSuffix(preview, "…") {
		t.Fatalf("preview length %d", len([]rune(preview)))
	}
}

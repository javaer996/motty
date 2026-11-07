package mobilecore

import (
	"encoding/json"
	"testing"

	"motty/internal/sshconn"
)

func TestHostPasswordLivesInTheVault(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	saved := callApp(t, app, "hosts.save", `{"label":"web","address":"example.com","user":"root","auth":"password"}`)
	if !saved.OK {
		t.Fatal(saved.Error)
	}
	var host Host
	if err := json.Unmarshal(saved.Result, &host); err != nil {
		t.Fatal(err)
	}

	set := callApp(t, app, "hosts.password", `{"id":"`+host.ID+`","password":"s3cret"}`)
	if !set.OK {
		t.Fatalf("hosts.password failed: %+v", set.Error)
	}
	if value, found, err := app.data.secretGet(hostSecretName(host.ID, hostPasswordPart)); err != nil || !found || value != "s3cret" {
		t.Fatalf("vault password = %q, %v, %v", value, found, err)
	}
	status := callApp(t, app, "hosts.passwordStatus", `{"id":"`+host.ID+`"}`)
	var passwordStatus struct {
		Saved bool `json:"saved"`
	}
	if err := json.Unmarshal(status.Result, &passwordStatus); err != nil || !passwordStatus.Saved {
		t.Fatalf("status = %s", status.Result)
	}
	// The password is not part of the host record that syncs and backs up.
	listed := callApp(t, app, "hosts.list", "")
	if string(listed.Result) == "" {
		t.Fatal("hosts.list returned nothing")
	}
	var list struct {
		Hosts []Host `json:"hosts"`
	}
	if err := json.Unmarshal(listed.Result, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Hosts) != 1 || list.Hosts[0].Auth != "password" {
		t.Fatalf("hosts = %+v", list.Hosts)
	}

	// Switching to a private key drops the password, as the desktop does.
	host.Auth = "key"
	switched, err := json.Marshal(host)
	if err != nil {
		t.Fatal(err)
	}
	if result := callApp(t, app, "hosts.save", string(switched)); !result.OK {
		t.Fatal(result.Error)
	}
	if _, found, _ := app.data.secretGet(hostSecretName(host.ID, hostPasswordPart)); found {
		t.Fatal("the password survived a switch to a private key")
	}

	// Deleting the host drops its secrets too.
	if result := callApp(t, app, "hosts.save", `{"id":"`+host.ID+`","label":"web","address":"example.com","user":"root","auth":"password"}`); !result.OK {
		t.Fatal(result.Error)
	}
	if result := callApp(t, app, "hosts.password", `{"id":"`+host.ID+`","password":"again"}`); !result.OK {
		t.Fatal(result.Error)
	}
	if result := callApp(t, app, "hosts.delete", `{"id":"`+host.ID+`"}`); !result.OK {
		t.Fatal(result.Error)
	}
	if _, found, _ := app.data.secretGet(hostSecretName(host.ID, hostPasswordPart)); found {
		t.Fatal("the password survived deleting the host")
	}
}

func TestSavedPasswordIsStagedUntilTheLoginSucceeds(t *testing.T) {
	data := openTestData(t)
	manager := &sessionManager{data: data}
	session := &session{host: Host{ID: "host_test"}, manager: manager}
	session.promptQuestions = []sshconn.Question{{Text: "Password:", Secret: true}}

	session.stagePasswordSave("first-password")
	session.discardPendingSecret()
	session.commitPendingSecret()
	if _, found, _ := data.secretGet(hostSecretName("host_test", hostPasswordPart)); found {
		t.Fatal("a discarded password was saved")
	}

	session.stagePasswordSave("first-password")
	session.commitPendingSecret()
	if value, found, err := data.secretGet(hostSecretName("host_test", hostPasswordPart)); err != nil || !found || value != "first-password" {
		t.Fatalf("saved password = %q, %v, %v", value, found, err)
	}

	// A one-time code is never kept as the host's password.
	session.promptQuestions = []sshconn.Question{{Text: "Verification code:", Secret: true}}
	session.stagePasswordSave("123456")
	session.commitPendingSecret()
	if value, _, _ := data.secretGet(hostSecretName("host_test", hostPasswordPart)); value != "first-password" {
		t.Fatalf("a one-time code replaced the password: %q", value)
	}
}

func TestPasswordQuestions(t *testing.T) {
	cases := []struct {
		name      string
		questions []sshconn.Question
		want      bool
	}{
		{"plain", []sshconn.Question{{Text: "Password:", Secret: true}}, true},
		{"empty wording", []sshconn.Question{{Text: "", Secret: true}}, true},
		{"chinese", []sshconn.Question{{Text: "密码：", Secret: true}}, true},
		{"otp", []sshconn.Question{{Text: "Verification code:", Secret: true}}, false},
		{"public answer", []sshconn.Question{{Text: "Password:", Secret: false}}, false},
		{"two questions", []sshconn.Question{{Text: "Password:", Secret: true}, {Text: "Token:", Secret: true}}, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := isPasswordQuestion(test.questions); got != test.want {
				t.Fatalf("isPasswordQuestion(%v) = %t, want %t", test.questions, got, test.want)
			}
		})
	}
}

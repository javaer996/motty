package mobilecore

import (
	"encoding/json"
	"testing"

	"motty/internal/sshconn"
)

// fakeSession installs a session that is not connected: completion only needs
// its host and terminal, and the line is passed explicitly here.
func fakeSession(t *testing.T, app *App, id uint32, host Host) {
	t.Helper()
	s := &session{
		id:      id,
		manager: app.sessions,
		phase:   sshconn.PhaseFailed,
		host:    host,
		done:    make(chan struct{}),
		wake:    make(chan struct{}, 1),
	}
	app.sessions.mu.Lock()
	app.sessions.sessions[id] = s
	app.sessions.mu.Unlock()
}

func completeLine(t *testing.T, app *App, id uint32, line, locale string) []completionCandidateJSON {
	t.Helper()
	params, err := json.Marshal(map[string]any{"sessionId": id, "line": line, "locale": locale})
	if err != nil {
		t.Fatal(err)
	}
	envelope := callApp(t, app, "completion.complete", string(params))
	if !envelope.OK {
		t.Fatalf("completion.complete failed: %+v", envelope.Error)
	}
	var result struct {
		Candidates []completionCandidateJSON `json:"candidates"`
		Suppressed bool                      `json:"suppressed"`
	}
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Suppressed {
		t.Fatalf("%q was suppressed", line)
	}
	return result.Candidates
}

func TestCompletionOffersParameterCombinationsInTheUiLanguage(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	fakeSession(t, app, 11, Host{ID: "host-11", Address: "example.com"})

	candidates := completeLine(t, app, 11, "git log --on", "zh-Hans")
	var combo *completionCandidateJSON
	for index := range candidates {
		if candidates[index].Kind == "combo" {
			combo = &candidates[index]
			break
		}
	}
	if combo == nil {
		t.Fatalf("no combination for git log: %+v", candidates)
	}
	if combo.Desc != "紧凑的提交图" {
		t.Fatalf("combination description = %q, want the Chinese one", combo.Desc)
	}
	if combo.Insert != "eline --graph --decorate -20" {
		t.Fatalf("combination insert = %q", combo.Insert)
	}

	// Catalog descriptions are translated the same way.
	catalog := completeLine(t, app, 11, "c", "zh-Hant")
	found := false
	for _, candidate := range catalog {
		if candidate.Text == "cp" {
			found = true
			if candidate.Desc != "複製檔案和目錄" {
				t.Fatalf("cp description = %q", candidate.Desc)
			}
		}
	}
	if !found {
		t.Fatalf("the catalog did not offer cp: %+v", catalog)
	}

	// The English UI keeps the catalog's own text.
	english := completeLine(t, app, 11, "git log --on", "en")
	if len(english) == 0 || english[0].Desc != "Compact commit graph" {
		t.Fatalf("english combination = %+v", english)
	}
}

// An empty prompt used to offer the alphabetically first catalog roots (acpi,
// acpid, adb...). The commands the host actually ran are the useful answer.
func TestCompletionUsesHistoryOnAnEmptyPrompt(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	fakeSession(t, app, 12, Host{ID: "host-12", Address: "example.com"})
	app.history.record("host-12", "netstat -tulnp")
	app.history.record("host-12", "docker ps -a")

	candidates := completeLine(t, app, 12, "", "zh-Hans")
	if len(candidates) != 2 {
		t.Fatalf("empty prompt offered %+v", candidates)
	}
	if candidates[0].Text != "docker ps -a" || candidates[0].Insert != "docker ps -a" {
		t.Fatalf("newest command not first: %+v", candidates)
	}
	if candidates[1].Text != "netstat -tulnp" {
		t.Fatalf("second command = %q", candidates[1].Text)
	}
	if candidates[0].Desc != "来自你的命令历史" {
		t.Fatalf("history description = %q", candidates[0].Desc)
	}
	if candidates[0].Kind != "command" {
		t.Fatalf("history kind = %q", candidates[0].Kind)
	}
}

// A command typed many times is a better hint than one typed once, even when
// the single use is newer.
func TestCompletionRanksHistoryByUse(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	fakeSession(t, app, 13, Host{ID: "host-13", Address: "example.com"})
	app.history.record("host-13", "docker ps")
	app.history.record("host-13", "docker ps -a")
	app.history.record("host-13", "docker ps -a")
	app.history.record("host-13", "docker ps -a")

	candidates := completeLine(t, app, 13, "docker p", "en")
	if len(candidates) == 0 || candidates[0].Text != "docker ps -a" {
		t.Fatalf("most used command not first: %+v", candidates)
	}
	if candidates[0].Desc != "From your history" {
		t.Fatalf("first candidate is not history: %+v", candidates)
	}
}

// A host with no history of its own still gets the commands typed on the other
// hosts.
func TestCompletionFallsBackToOtherHostsHistory(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	fakeSession(t, app, 14, Host{ID: "host-14", Address: "example.com"})
	app.history.record("another-host", "systemctl restart nginx")

	candidates := completeLine(t, app, 14, "", "en")
	if len(candidates) == 0 || candidates[0].Text != "systemctl restart nginx" {
		t.Fatalf("cross-host history = %+v", candidates)
	}
}

// The imported snapshot has no netstat; the hand-written essentials take over
// once the command is unknown to it.
func TestCompletionFallsBackToEssentials(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	fakeSession(t, app, 15, Host{ID: "host-15", Address: "example.com"})

	candidates := completeLine(t, app, 15, "netstat -", "en")
	texts := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		texts[candidate.Text] = true
	}
	for _, want := range []string{"-t", "-u", "-l", "-n", "-p"} {
		if !texts[want] {
			t.Fatalf("netstat %s missing from %+v", want, candidates)
		}
	}
}

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

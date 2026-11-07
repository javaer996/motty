package mobilecore

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"motty/internal/sshconn"
)

func saveTestHost(t *testing.T, app *App, address string, port uint16) Host {
	t.Helper()
	saved := callApp(t, app, "hosts.save", `{"label":"test","address":`+quoteJSON(address)+
		`,"user":"me","port":`+strconv.Itoa(int(port))+`,"auth":"password"}`)
	if !saved.OK {
		t.Fatalf("hosts.save failed: %+v", saved.Error)
	}
	var host Host
	if err := json.Unmarshal(saved.Result, &host); err != nil {
		t.Fatal(err)
	}
	return host
}

func waitForShell(t *testing.T, app *App, sessionID uint32) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		body := `{"id":` + strconv.Itoa(int(sessionID)) + `,"data":""}`
		if env := callApp(t, app, "sessions.input", body); env.OK {
			// The prompt may still be arriving; the recorder requires a quiet
			// terminal before it arms.
			time.Sleep(250 * time.Millisecond)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the shell never accepted input")
}

func typeInput(t *testing.T, app *App, sessionID uint32, text string) {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	body := `{"id":` + strconv.Itoa(int(sessionID)) + `,"data":` + quoteJSON(encoded) + `}`
	if env := callApp(t, app, "sessions.input", body); !env.OK {
		t.Fatalf("sessions.input failed: %+v", env.Error)
	}
}

type historyListResult struct {
	Entries []historyEntryJSON `json:"entries"`
}

func listHistory(t *testing.T, app *App, hostID string) []historyEntryJSON {
	t.Helper()
	env := callApp(t, app, "history.list", `{"hostId":`+quoteJSON(hostID)+`}`)
	if !env.OK {
		t.Fatalf("history.list failed: %+v", env.Error)
	}
	var result historyListResult
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result.Entries
}

func TestHistoryRecordsTypedCommands(t *testing.T) {
	serverHost, serverPort := startForwardServer(t, "secret")
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	host := saveTestHost(t, app, serverHost, serverPort)
	sessionID := openConnectedSession(t, app, host.ID)
	waitForShell(t, app, sessionID)

	typeInput(t, app, sessionID, "echo hi")
	time.Sleep(150 * time.Millisecond)
	typeInput(t, app, sessionID, "\r")

	deadline := time.Now().Add(5 * time.Second)
	var entries []historyEntryJSON
	for time.Now().Before(deadline) {
		entries = listHistory(t, app, host.ID)
		if len(entries) > 0 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if len(entries) != 1 || entries[0].Command != "echo hi" {
		t.Fatalf("history = %+v", entries)
	}

	completed := callApp(t, app, "completion.complete",
		`{"sessionId":`+strconv.Itoa(int(sessionID))+`,"line":"echo h"}`)
	if !completed.OK {
		t.Fatalf("completion.complete failed: %+v", completed.Error)
	}
	var completionResult struct {
		Line       string                    `json:"line"`
		Candidates []completionCandidateJSON `json:"candidates"`
		Suppressed bool                      `json:"suppressed"`
	}
	if err := json.Unmarshal(completed.Result, &completionResult); err != nil {
		t.Fatal(err)
	}
	if completionResult.Line != "echo h" || completionResult.Suppressed {
		t.Fatalf("completion = %+v", completionResult)
	}
	sawHistory := false
	for _, candidate := range completionResult.Candidates {
		if candidate.Insert == "i" {
			sawHistory = true
		}
	}
	if !sawHistory {
		t.Fatalf("the typed command was not offered: %+v", completionResult.Candidates)
	}

	deleted := callApp(t, app, "history.delete",
		`{"hostId":`+quoteJSON(host.ID)+`,"command":"echo hi"}`)
	if !deleted.OK {
		t.Fatalf("history.delete failed: %+v", deleted.Error)
	}
	if entries = listHistory(t, app, host.ID); len(entries) != 0 {
		t.Fatalf("history after delete = %+v", entries)
	}
	if cleared := callApp(t, app, "history.clear", ""); !cleared.OK {
		t.Fatalf("history.clear failed: %+v", cleared.Error)
	}
}

type logListResult struct {
	Records []logRecordJSON `json:"records"`
	Matched int64           `json:"matched"`
	More    bool            `json:"more"`
}

func listLogs(t *testing.T, app *App) logListResult {
	t.Helper()
	env := callApp(t, app, "logs.list", `{"limit":50}`)
	if !env.OK {
		t.Fatalf("logs.list failed: %+v", env.Error)
	}
	var result logListResult
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSessionJournalLifecycle(t *testing.T) {
	serverHost, serverPort := startForwardServer(t, "secret")
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	host := saveTestHost(t, app, serverHost, serverPort)
	sessionID := openConnectedSession(t, app, host.ID)

	deadline := time.Now().Add(5 * time.Second)
	var record logRecordJSON
	var seen []logRecordJSON
	for time.Now().Before(deadline) {
		seen = listLogs(t, app).Records
		for _, current := range seen {
			if current.Status == "active" {
				record = current
			}
		}
		if record.ID != "" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if record.ID == "" {
		t.Fatalf("no active journal record appeared; saw %+v", seen)
	}
	if record.Host != "test" || record.Account != "me" || record.Protocol != "ssh" {
		t.Fatalf("record = %+v", record)
	}
	if record.EndedAt != 0 || record.DurationMillis < 0 {
		t.Fatalf("an active record must be open: %+v", record)
	}

	eventsEnv := callApp(t, app, "logs.events", `{"id":`+quoteJSON(record.ID)+`}`)
	if !eventsEnv.OK {
		t.Fatalf("logs.events failed: %+v", eventsEnv.Error)
	}
	var events struct {
		Events []logEventJSON `json:"events"`
	}
	if err := json.Unmarshal(eventsEnv.Result, &events); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, event := range events.Events {
		kinds[event.Kind] = true
	}
	for _, want := range []string{"started", "connected"} {
		if !kinds[want] {
			t.Fatalf("timeline lacks %q: %+v", want, events.Events)
		}
	}

	if env := callApp(t, app, "logs.favorite",
		`{"id":`+quoteJSON(record.ID)+`,"favorite":true}`); !env.OK {
		t.Fatalf("logs.favorite failed: %+v", env.Error)
	}
	favorites := callApp(t, app, "logs.list", `{"favorite":true}`)
	if !favorites.OK {
		t.Fatalf("logs.list favorite failed: %+v", favorites.Error)
	}
	var favoriteResult logListResult
	if err := json.Unmarshal(favorites.Result, &favoriteResult); err != nil {
		t.Fatal(err)
	}
	if len(favoriteResult.Records) != 1 || !favoriteResult.Records[0].Favorite {
		t.Fatalf("favorites = %+v", favoriteResult.Records)
	}

	closed := callApp(t, app, "sessions.close", `{"id":`+strconv.Itoa(int(sessionID))+`}`)
	if !closed.OK {
		t.Fatalf("sessions.close failed: %+v", closed.Error)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		result := listLogs(t, app)
		if len(result.Records) == 1 {
			record = result.Records[0]
			if record.Status == "ended" && record.Reason == "user-closed" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the record never ended: %+v", record)
		}
		time.Sleep(30 * time.Millisecond)
	}
	if record.EndedAt == 0 {
		t.Fatalf("an ended record has no end time: %+v", record)
	}

	eventsEnv = callApp(t, app, "logs.events", `{"id":`+quoteJSON(record.ID)+`}`)
	events.Events = nil
	if err := json.Unmarshal(eventsEnv.Result, &events); err != nil {
		t.Fatal(err)
	}
	kinds = map[string]bool{}
	for _, event := range events.Events {
		kinds[event.Kind] = true
	}
	if !kinds["finished"] {
		t.Fatalf("timeline lacks finished: %+v", events.Events)
	}

	stats := callApp(t, app, "logs.stats", "")
	if !stats.OK {
		t.Fatalf("logs.stats failed: %+v", stats.Error)
	}
	var statsResult struct {
		Records   int `json:"records"`
		Favorites int `json:"favorites"`
	}
	if err := json.Unmarshal(stats.Result, &statsResult); err != nil {
		t.Fatal(err)
	}
	if statsResult.Records != 1 || statsResult.Favorites != 1 {
		t.Fatalf("stats = %+v", statsResult)
	}

	if cleared := callApp(t, app, "logs.clear", `{"includeFavorites":true}`); !cleared.OK {
		t.Fatalf("logs.clear failed: %+v", cleared.Error)
	}
	if result := listLogs(t, app); len(result.Records) != 0 {
		t.Fatalf("journal after clear = %+v", result.Records)
	}
}

func TestCompletionWorksWithoutAHistoryFile(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	s := &session{
		id:      9,
		manager: app.sessions,
		phase:   sshconn.PhaseFailed,
		host:    Host{ID: "host-9", Address: "example.com"},
		done:    make(chan struct{}),
		wake:    make(chan struct{}, 1),
	}
	app.sessions.mu.Lock()
	app.sessions.sessions[9] = s
	app.sessions.mu.Unlock()

	env := callApp(t, app, "completion.complete", `{"sessionId":9,"line":"git che"}`)
	if !env.OK {
		t.Fatalf("completion.complete failed: %+v", env.Error)
	}
	var result struct {
		Candidates []completionCandidateJSON `json:"candidates"`
	}
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) == 0 {
		t.Fatal("the built-in catalog offered nothing for a git command")
	}
}

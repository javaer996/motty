package mobilecore

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"motty/internal/ai/llm"
	"motty/internal/sshconn"
	"motty/internal/term"
)

// fakeProvider replays scripted model turns.
type fakeProvider struct {
	mu       sync.Mutex
	turns    [][]llm.Event
	requests []llm.Request
}

func (p *fakeProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	if len(p.turns) == 0 {
		p.mu.Unlock()
		return nil, errors.New("no scripted turn left")
	}
	events := p.turns[0]
	p.turns = p.turns[1:]
	p.mu.Unlock()
	ch := make(chan llm.Event, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

func (p *fakeProvider) Models(context.Context) ([]string, error) {
	return []string{"fake-model"}, nil
}

func (p *fakeProvider) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func addTestSession(t *testing.T, app *App, id uint32) {
	t.Helper()
	s := &session{
		id:       id,
		host:     Host{ID: "host-test", Label: "test", Address: "example.test", User: "root", Port: 22},
		manager:  app.sessions,
		terminal: term.New(20, 5, 100),
		cols:     20,
		rows:     5,
		phase:    sshconn.PhaseConnected,
		run:      newSessionRun(),
		wake:     make(chan struct{}, 1),
	}
	app.sessions.mu.Lock()
	app.sessions.sessions[id] = s
	app.sessions.activeID = id
	app.sessions.mu.Unlock()
}

func useFakeProvider(t *testing.T, app *App, provider llm.Provider) {
	t.Helper()
	if _, err := app.ai.settings.save(aiSettings{Providers: []aiProvider{{Provider: "openai", Model: "fake-model"}}}); err != nil {
		t.Fatal(err)
	}
	app.ai.provider = func(aiProvider) (llm.Provider, error) { return provider, nil }
}

// collectEvents drains events until until matches, returning everything read.
func collectEvents(t *testing.T, app *App, until func(map[string]any) bool) []map[string]any {
	t.Helper()
	buffer := make([]byte, 64*1024)
	var events []map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n := app.PollEvent(buffer)
		switch {
		case n > 0:
			var event map[string]any
			if err := json.Unmarshal(buffer[:n], &event); err != nil {
				t.Fatalf("event is not JSON: %v", err)
			}
			events = append(events, event)
			if until != nil && until(event) {
				return events
			}
		case n < 0:
			buffer = make([]byte, -n)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	t.Fatalf("timed out; events so far: %v", events)
	return nil
}

func stateDone(event map[string]any) bool {
	return event["type"] == "ai.state" && event["state"] == "done"
}

func TestAIRunStreamsText(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	fake := &fakeProvider{turns: [][]llm.Event{{
		{Kind: llm.EventText, Text: "Hello"},
		{Kind: llm.EventText, Text: " there"},
		{Kind: llm.EventDone, Usage: llm.Usage{InputTokens: 3, OutputTokens: 2}, StopReason: "stop"},
	}}}
	useFakeProvider(t, app, fake)
	addTestSession(t, app, 7)

	runID, err := app.ai.send(7, "hi")
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, app, stateDone)

	text := ""
	for _, event := range events {
		if event["type"] == "ai.text" {
			text += event["text"].(string)
			if uint64(event["runId"].(float64)) != runID {
				t.Fatalf("event runId = %v, want %d", event["runId"], runID)
			}
			if event["sessionId"].(float64) != 7 {
				t.Fatalf("event sessionId = %v, want 7", event["sessionId"])
			}
		}
	}
	if text != "Hello there" {
		t.Fatalf("streamed text = %q", text)
	}
	if fake.requestCount() != 1 {
		t.Fatalf("provider got %d requests, want 1", fake.requestCount())
	}
	if got := app.ai.history["host-test"]; len(got) != 2 {
		t.Fatalf("history has %d messages, want user and assistant", len(got))
	}
}

func TestAIRunToolCallWithoutShell(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	fake := &fakeProvider{turns: [][]llm.Event{
		{
			{Kind: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call-1", Name: "terminal_read", Arguments: `{"scope":"screen","lines":10}`,
			}},
			{Kind: llm.EventDone, StopReason: "tool_calls"},
		},
		{
			{Kind: llm.EventText, Text: "The screen is blank."},
			{Kind: llm.EventDone, StopReason: "stop"},
		},
	}}
	useFakeProvider(t, app, fake)
	addTestSession(t, app, 8)

	if _, err := app.ai.send(8, "what is on screen?"); err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, app, stateDone)

	var sawTool, sawResult bool
	for _, event := range events {
		switch event["type"] {
		case "ai.tool":
			sawTool = event["name"] == "terminal_read"
		case "ai.toolResult":
			sawResult = event["id"] == "call-1" && event["isError"] == false
		}
	}
	if !sawTool || !sawResult {
		t.Fatalf("tool events missing: %v", events)
	}
	if fake.requestCount() != 2 {
		t.Fatalf("provider got %d requests, want 2", fake.requestCount())
	}
	history := app.ai.history["host-test"]
	if len(history) < 4 {
		t.Fatalf("history has %d messages, want the tool round too", len(history))
	}
}

func TestAIRunAsksBeforeDangerousCommand(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	fake := &fakeProvider{turns: [][]llm.Event{
		{
			{Kind: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call-danger", Name: "terminal_exec", Arguments: `{"command":"rm -rf /tmp/motty-test","timeout_seconds":5}`,
			}},
			{Kind: llm.EventDone, StopReason: "tool_calls"},
		},
		{
			{Kind: llm.EventText, Text: "The command could not run."},
			{Kind: llm.EventDone, StopReason: "stop"},
		},
	}}
	useFakeProvider(t, app, fake)
	addTestSession(t, app, 9)

	if _, err := app.ai.send(9, "clean up the temp directory"); err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, app, func(event map[string]any) bool {
		return event["type"] == "ai.approval"
	})
	var approval map[string]any
	for _, event := range events {
		if event["type"] == "ai.approval" {
			approval = event
		}
	}
	if approval == nil {
		t.Fatalf("no approval event in %v", events)
	}
	if approval["level"] != "High" {
		t.Fatalf("approval level = %v, want High", approval["level"])
	}
	runID := uint64(approval["runId"].(float64))
	approvalID, _ := approval["approvalId"].(string)
	if approvalID == "" {
		t.Fatal("approval has no id")
	}
	if err := app.ai.approve(runID, approvalID, true, "", false); err != nil {
		t.Fatal(err)
	}
	collectEvents(t, app, stateDone)
	if fake.requestCount() != 2 {
		t.Fatalf("provider got %d requests, want 2", fake.requestCount())
	}
}

package mobilecore

import (
	"encoding/json"
	"strconv"
	"testing"

	"motty/internal/ai/llm"
	"motty/internal/sshconn"
)

// A conversation belongs to the host, not to one SSH tab: reconnecting or
// restarting the app must restore both what the user sees and the context the
// model receives.
func TestAIConversationPersistsAcrossSessions(t *testing.T) {
	dir := t.TempDir()
	app, err := Start(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	first := &fakeProvider{turns: [][]llm.Event{{
		{Kind: llm.EventText, Text: "hello from the first session"},
		{Kind: llm.EventDone, StopReason: "stop"},
	}}}
	useFakeProvider(t, app, first)
	addTestSession(t, app, 7)
	if _, err := app.ai.send(7, "first question"); err != nil {
		t.Fatal(err)
	}
	collectEvents(t, app, stateDone)

	list := callApp(t, app, "ai.conversations", `{"sessionId":7}`)
	if !list.OK {
		t.Fatalf("ai.conversations failed: %+v", list.Error)
	}
	var listed struct {
		Conversations []aiConversationSummary `json:"conversations"`
	}
	if err := json.Unmarshal(list.Result, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Conversations) != 1 || !listed.Conversations[0].Active {
		t.Fatalf("conversations = %+v", listed.Conversations)
	}
	conversationID := listed.Conversations[0].ID
	if listed.Conversations[0].Title != "first question" {
		t.Fatalf("title = %q", listed.Conversations[0].Title)
	}
	app.Close()

	app, err = Start(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	second := &fakeProvider{turns: [][]llm.Event{
		{
			{Kind: llm.EventText, Text: "still here"},
			{Kind: llm.EventDone, StopReason: "stop"},
		},
		{
			{Kind: llm.EventText, Text: "fresh start"},
			{Kind: llm.EventDone, StopReason: "stop"},
		},
		{
			{Kind: llm.EventText, Text: "old context"},
			{Kind: llm.EventDone, StopReason: "stop"},
		},
	}}
	useFakeProvider(t, app, second)
	addTestSession(t, app, 99)

	// The new session sees the saved conversation.
	loaded := callApp(t, app, "ai.conversation.get", `{"sessionId":99,"conversationId":0}`)
	if !loaded.OK {
		t.Fatalf("ai.conversation.get failed: %+v", loaded.Error)
	}
	var view aiConversationView
	if err := json.Unmarshal(loaded.Result, &view); err != nil {
		t.Fatal(err)
	}
	if view.ID != conversationID || len(view.Messages) != 2 {
		t.Fatalf("loaded conversation = %+v, want id %d with two messages", view, conversationID)
	}

	// Sending continues from the restored context.
	if _, err := app.ai.send(99, "second question"); err != nil {
		t.Fatal(err)
	}
	collectEvents(t, app, stateDone)
	requests := second.requests
	if len(requests) != 1 {
		t.Fatalf("provider got %d requests, want 1", len(requests))
	}
	context := requests[0].Messages
	if len(context) != 3 || context[0].Text != "first question" || context[1].Text != "hello from the first session" || context[2].Text != "second question" {
		t.Fatalf("restored context = %+v", context)
	}

	// A new conversation clears the context but keeps the old thread in the
	// history list.
	if reset := callApp(t, app, "ai.reset", `{"sessionId":99}`); !reset.OK {
		t.Fatalf("ai.reset failed: %+v", reset.Error)
	}
	if _, err := app.ai.send(99, "new question"); err != nil {
		t.Fatal(err)
	}
	collectEvents(t, app, stateDone)
	if got := second.requests[1].Messages; len(got) != 1 || got[0].Text != "new question" {
		t.Fatalf("new conversation context = %+v", got)
	}
	all := callApp(t, app, "ai.conversations", `{"sessionId":99}`)
	if !all.OK {
		t.Fatalf("ai.conversations failed: %+v", all.Error)
	}
	if err := json.Unmarshal(all.Result, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Conversations) != 2 {
		t.Fatalf("history has %d conversations, want 2", len(listed.Conversations))
	}
	active := 0
	for _, conversation := range listed.Conversations {
		if conversation.Active {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("history has %d active conversations", active)
	}

	// Opening the older thread makes it current again and restores its context.
	opened := callApp(t, app, "ai.conversation.get", `{"sessionId":99,"conversationId":`+strconv.FormatInt(conversationID, 10)+`}`)
	if !opened.OK {
		t.Fatalf("opening the saved conversation failed: %+v", opened.Error)
	}
	if _, err := app.ai.send(99, "continue the old thread"); err != nil {
		t.Fatal(err)
	}
	collectEvents(t, app, stateDone)
	continued := second.requests[2].Messages
	if len(continued) != 5 || continued[0].Text != "first question" || continued[1].Text != "hello from the first session" ||
		continued[3].Text != "still here" || continued[4].Text != "continue the old thread" {
		t.Fatalf("context after opening history = %+v", continued)
	}

	// Deleting the active conversation leaves the other one alone.
	if deleted := callApp(t, app, "ai.conversation.delete", `{"sessionId":99,"conversationId":`+strconv.FormatInt(conversationID, 10)+`}`); !deleted.OK {
		t.Fatalf("ai.conversation.delete failed: %+v", deleted.Error)
	}
	remaining := callApp(t, app, "ai.conversations", `{"sessionId":99}`)
	if !remaining.OK {
		t.Fatalf("ai.conversations failed: %+v", remaining.Error)
	}
	if err := json.Unmarshal(remaining.Result, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Conversations) != 1 {
		t.Fatalf("history after delete = %+v", listed.Conversations)
	}
}

func TestAIHistoryIsScopedToHost(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	fake := &fakeProvider{turns: [][]llm.Event{{
		{Kind: llm.EventText, Text: "ok"},
		{Kind: llm.EventDone, StopReason: "stop"},
	}}}
	useFakeProvider(t, app, fake)
	addTestSession(t, app, 1)
	if _, err := app.ai.send(1, "question for host-test"); err != nil {
		t.Fatal(err)
	}
	collectEvents(t, app, stateDone)

	other := &session{
		id:      2,
		host:    Host{ID: "host-other", Label: "other", Address: "other.test", User: "root", Port: 22},
		manager: app.sessions,
		phase:   sshconn.PhaseConnected,
		done:    make(chan struct{}),
		wake:    make(chan struct{}, 1),
	}
	app.sessions.mu.Lock()
	app.sessions.sessions[2] = other
	app.sessions.mu.Unlock()

	list := callApp(t, app, "ai.conversations", `{"sessionId":2}`)
	if !list.OK {
		t.Fatalf("ai.conversations failed: %+v", list.Error)
	}
	var listed struct {
		Conversations []aiConversationSummary `json:"conversations"`
	}
	if err := json.Unmarshal(list.Result, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Conversations) != 0 {
		t.Fatalf("another host sees %+v", listed.Conversations)
	}
}

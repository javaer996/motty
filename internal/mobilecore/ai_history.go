package mobilecore

import (
	"encoding/json"
	"strconv"
	"strings"

	"motty/internal/ai/gate"
	"motty/internal/ai/llm"
	aistore "motty/internal/ai/store"
)

// Conversations are kept per host in <DataDir>/ai.db, the same sqlite schema
// the desktop version uses. A session is only one view onto that host's
// conversation: closing the terminal or reconnecting does not lose the
// assistant's context, and reopening the assistant shows what came before.

const activeConversationSetting = "ios.activeConversation."

func activeConversationKey(hostID string) string {
	return activeConversationSetting + hostID
}

// storedToolMeta is the part of a message this app adds to the opaque
// ToolJSON column. Calls and CallID are the desktop layout; ios_host marks
// which saved host owns the conversation, so history can be filtered without
// adding a column to the shared schema.
type storedToolMeta struct {
	Calls  []llm.ToolCall `json:"calls,omitempty"`
	CallID string         `json:"call_id,omitempty"`
	HostID string         `json:"ios_host,omitempty"`
}

func toStoredMessage(message llm.Message, hostID string) aistore.StoredMessage {
	stored := aistore.StoredMessage{Role: string(message.Role), Text: message.Text}
	meta := storedToolMeta{Calls: message.ToolCalls, CallID: message.ToolCallID}
	// A user question and an assistant message that calls tools both anchor
	// the conversation to its host. Tool results carry only their call id.
	if message.Role == llm.RoleUser || len(message.ToolCalls) > 0 {
		meta.HostID = hostID
	}
	if len(meta.Calls) > 0 || meta.CallID != "" || meta.HostID != "" {
		stored.ToolJSON, _ = json.Marshal(meta)
	}
	return stored
}

func fromStoredMessage(stored aistore.StoredMessage) (llm.Message, storedToolMeta) {
	message := llm.Message{Role: llm.Role(stored.Role), Text: stored.Text}
	var meta storedToolMeta
	if len(stored.ToolJSON) > 0 {
		if json.Unmarshal(stored.ToolJSON, &meta) == nil {
			message.ToolCalls, message.ToolCallID = meta.Calls, meta.CallID
		}
	}
	return message, meta
}

// activeConversation returns the conversation a host is currently continuing.
// Zero means the next message starts a new one.
func (s *aiState) activeConversation(hostID string) int64 {
	s.mu.Lock()
	id, cached := s.convs[hostID]
	s.mu.Unlock()
	if cached {
		return id
	}
	if s.store == nil {
		return 0
	}
	raw, ok, err := s.store.Setting(activeConversationKey(hostID))
	if err != nil || !ok {
		return 0
	}
	id, _ = strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	s.mu.Lock()
	if existing, ok := s.convs[hostID]; ok {
		id = existing
	} else {
		s.convs[hostID] = id
	}
	s.mu.Unlock()
	return id
}

func (s *aiState) setActiveConversation(hostID string, id int64) {
	s.mu.Lock()
	s.convs[hostID] = id
	s.mu.Unlock()
	if s.store != nil {
		_ = s.store.SetSetting(activeConversationKey(hostID), strconv.FormatInt(id, 10))
	}
}

// historyFor returns the message history the assistant should see for one
// host. The first call after a restart restores it from ai.db.
func (s *aiState) historyFor(hostID string) []llm.Message {
	s.mu.Lock()
	if history, ok := s.history[hostID]; ok {
		out := append([]llm.Message(nil), history...)
		s.mu.Unlock()
		return out
	}
	s.mu.Unlock()

	var loaded []llm.Message
	if id := s.activeConversation(hostID); id != 0 && s.store != nil {
		if rows, err := s.store.Messages(id); err == nil {
			for _, row := range rows {
				message, _ := fromStoredMessage(row)
				loaded = append(loaded, message)
			}
		}
	}

	s.mu.Lock()
	if history, ok := s.history[hostID]; ok {
		// Another run loaded it while this one read the database.
		s.mu.Unlock()
		return append([]llm.Message(nil), history...)
	}
	s.history[hostID] = loaded
	s.mu.Unlock()
	return append([]llm.Message(nil), loaded...)
}

// recordRun adds the new turn to memory and to the conversation on disk.
func (s *aiState) recordRun(hostID string, history, messages []llm.Message, target gate.SessionInfo) {
	full := append(history, messages...)
	s.mu.Lock()
	s.history[hostID] = full
	s.mu.Unlock()
	if len(messages) == 0 {
		return
	}
	s.persist(hostID, target, messages)
}

func (s *aiState) persist(hostID string, target gate.SessionInfo, messages []llm.Message) {
	if s.store == nil {
		return
	}
	s.storeMu.Lock()
	defer s.storeMu.Unlock()

	id := s.activeConversation(hostID)
	if id == 0 {
		label := target.Title
		if strings.TrimSpace(label) == "" {
			label = target.Address
		}
		conversation, err := s.store.CreateConversation(conversationTitle(messages, label), label)
		if err != nil {
			return
		}
		id = conversation.ID
		s.setActiveConversation(hostID, id)
	}
	for _, message := range messages {
		if _, err := s.store.AppendMessage(id, toStoredMessage(message, hostID)); err != nil {
			return
		}
	}
}

func conversationTitle(messages []llm.Message, fallback string) string {
	for _, message := range messages {
		if message.Role != llm.RoleUser {
			continue
		}
		title := strings.Join(strings.Fields(message.Text), " ")
		if title == "" {
			continue
		}
		if runes := []rune(title); len(runes) > 60 {
			title = string(runes[:60])
		}
		return title
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	return "Assistant"
}

// reset starts a new conversation for the host. The previous one stays in the
// database and remains available from the history list.
func (s *aiState) reset(hostID string) {
	s.mu.Lock()
	s.history[hostID] = nil
	s.mu.Unlock()
	s.setActiveConversation(hostID, 0)
}

// aiConversationSummary is one row of the assistant history list.
type aiConversationSummary struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	HostLabel string `json:"hostLabel,omitempty"`
	Updated   int64  `json:"updated"` // Unix milliseconds
	Active    bool   `json:"active"`
}

// aiToolCallView is a tool call with the lower-case field names the Swift
// side decodes; llm.ToolCall itself has no JSON tags.
type aiToolCallView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// aiMessageView is one stored message in a form the chat UI renders directly.
type aiMessageView struct {
	Role       string           `json:"role"`
	Text       string           `json:"text,omitempty"`
	ToolCalls  []aiToolCallView `json:"toolCalls,omitempty"`
	ToolCallID string           `json:"toolCallId,omitempty"`
}

type aiConversationView struct {
	ID       int64           `json:"id"`
	Messages []aiMessageView `json:"messages"`
}

// conversations lists the saved conversations that belong to one host, most
// recently updated first.
func (s *aiState) conversations(hostID string) ([]aiConversationSummary, error) {
	if s.store == nil {
		return nil, nil
	}
	saved, err := s.store.Conversations("", aistore.MaxListLimit)
	if err != nil {
		return nil, err
	}
	active := s.activeConversation(hostID)
	out := make([]aiConversationSummary, 0, len(saved))
	for _, conversation := range saved {
		stored, err := s.store.Messages(conversation.ID)
		if err != nil {
			continue
		}
		owned := false
		for _, row := range stored {
			if _, meta := fromStoredMessage(row); meta.HostID == hostID {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}
		out = append(out, aiConversationSummary{
			ID:        conversation.ID,
			Title:     conversation.Title,
			HostLabel: conversation.SessionLabel,
			Updated:   conversation.Updated.UnixMilli(),
			Active:    conversation.ID == active,
		})
	}
	return out, nil
}

// conversation loads one saved conversation. A zero id means the host's
// active conversation; passing one explicitly makes it active so the next
// message continues from it.
func (s *aiState) conversation(hostID string, id int64) (aiConversationView, error) {
	if id == 0 {
		id = s.activeConversation(hostID)
	}
	if id == 0 || s.store == nil {
		return aiConversationView{}, nil
	}
	stored, err := s.store.Messages(id)
	if err != nil {
		return aiConversationView{}, errorf(CodeNotFound, "no assistant conversation with id %d", id)
	}
	owned := false
	messages := make([]llm.Message, 0, len(stored))
	view := aiConversationView{ID: id, Messages: make([]aiMessageView, 0, len(stored))}
	for _, row := range stored {
		message, meta := fromStoredMessage(row)
		if meta.HostID == hostID {
			owned = true
		}
		messages = append(messages, message)
		if message.Role == llm.RoleSystem {
			continue
		}
		if message.Text == "" && len(message.ToolCalls) == 0 && message.ToolCallID == "" {
			continue
		}
		calls := make([]aiToolCallView, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			calls = append(calls, aiToolCallView{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
		}
		view.Messages = append(view.Messages, aiMessageView{
			Role:       string(message.Role),
			Text:       message.Text,
			ToolCalls:  calls,
			ToolCallID: message.ToolCallID,
		})
	}
	if !owned {
		return aiConversationView{}, errorf(CodeNotFound, "no assistant conversation with id %d for this host", id)
	}
	s.mu.Lock()
	s.history[hostID] = messages
	s.mu.Unlock()
	s.setActiveConversation(hostID, id)
	return view, nil
}

// deleteConversation removes one saved conversation and reports whether it
// was the active one, which also clears the host's context.
func (s *aiState) deleteConversation(hostID string, id int64) (bool, error) {
	if id == 0 || s.store == nil {
		return false, errorf(CodeNotFound, "no assistant conversation with id %d", id)
	}
	stored, err := s.store.Messages(id)
	if err != nil {
		return false, errorf(CodeNotFound, "no assistant conversation with id %d", id)
	}
	owned := false
	for _, row := range stored {
		if _, meta := fromStoredMessage(row); meta.HostID == hostID {
			owned = true
			break
		}
	}
	if !owned {
		return false, errorf(CodeNotFound, "no assistant conversation with id %d for this host", id)
	}
	active := s.activeConversation(hostID) == id
	if err := s.store.DeleteConversation(id); err != nil {
		return false, err
	}
	if active {
		s.reset(hostID)
	}
	return active, nil
}

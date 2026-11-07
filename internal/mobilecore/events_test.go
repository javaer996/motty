package mobilecore

import (
	"encoding/json"
	"testing"
)

func TestEventQueueKeepsOrder(t *testing.T) {
	queue := newEventQueue(4)
	queue.push("first", map[string]any{"n": 1})
	queue.push("second", map[string]any{"n": 2})

	buffer := make([]byte, 256)
	for _, want := range []string{"first", "second"} {
		n := queue.poll(buffer)
		if n <= 0 {
			t.Fatalf("poll returned %d, want a buffered event", n)
		}
		var event Event
		if err := json.Unmarshal(buffer[:n], &event); err != nil {
			t.Fatalf("event is not JSON: %v", err)
		}
		if event["type"] != want {
			t.Fatalf("event type = %v, want %q", event["type"], want)
		}
	}
	if n := queue.poll(buffer); n != 0 {
		t.Fatalf("poll on an empty queue returned %d, want 0", n)
	}
}

func TestEventQueueReportsNeededSize(t *testing.T) {
	queue := newEventQueue(4)
	queue.push("state", map[string]any{"text": "hello"})

	need := queue.poll(make([]byte, 4))
	if need >= 0 {
		t.Fatalf("poll with a tiny buffer returned %d, want a negative needed size", need)
	}
	// The event stays queued until the caller has room for all of it.
	if n := queue.poll(make([]byte, -need)); n != -need {
		t.Fatalf("poll with the reported size returned %d, want %d", n, -need)
	}
}

func TestEventQueueDropsOldestWhenFull(t *testing.T) {
	queue := newEventQueue(2)
	queue.push("one", nil)
	queue.push("two", nil)
	queue.push("three", nil)
	if got := queue.droppedCount(); got != 1 {
		t.Fatalf("dropped = %d, want 1", got)
	}
	buffer := make([]byte, 256)
	var types []string
	for {
		n := queue.poll(buffer)
		if n <= 0 {
			break
		}
		var event Event
		if err := json.Unmarshal(buffer[:n], &event); err != nil {
			t.Fatal(err)
		}
		types = append(types, event["type"].(string))
	}
	if len(types) != 2 || types[0] != "two" || types[1] != "three" {
		t.Fatalf("remaining events = %v, want [two three]", types)
	}
}

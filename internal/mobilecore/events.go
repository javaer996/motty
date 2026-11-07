package mobilecore

import (
	"encoding/json"
	"sync"
)

// Event is one message the front end polls for. Type names the payload shape;
// the remaining fields are the payload itself.
type Event map[string]any

// eventQueue holds events until the front end polls. It is bounded: a front
// end that stops polling (the app is suspended, say) must not grow the queue
// without limit. Dropped counts what fell off the end.
type eventQueue struct {
	mu      sync.Mutex
	events  []json.RawMessage
	max     int
	dropped int
}

func newEventQueue(max int) *eventQueue {
	return &eventQueue{max: max}
}

// push adds one event. It never blocks and never returns an error: an event
// that does not fit is dropped, and dropped records how many.
func (q *eventQueue) push(kind string, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["type"] = kind
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.events) >= q.max {
		copy(q.events, q.events[1:])
		q.events[len(q.events)-1] = raw
		q.dropped++
		return
	}
	q.events = append(q.events, raw)
}

// poll copies the oldest event into dst and returns its length. It returns 0
// when the queue is empty, and -needed when dst is too small (the event stays
// queued). A negative return is the number of bytes required, negated.
func (q *eventQueue) poll(dst []byte) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.events) == 0 {
		return 0
	}
	raw := q.events[0]
	if len(dst) < len(raw) {
		return -len(raw)
	}
	n := copy(dst, raw)
	q.events = q.events[1:]
	return n
}

// droppedCount reports events lost to a full queue, for diagnostics.
func (q *eventQueue) droppedCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}

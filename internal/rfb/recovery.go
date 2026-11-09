package rfb

import (
	"fmt"
	"time"
)

const (
	updateWatchInterval = 100 * time.Millisecond
	inputRefreshDelay   = 900 * time.Millisecond
	spaceRefreshDelay   = 350 * time.Millisecond
	spaceRefreshRetry   = 700 * time.Millisecond
	progressTimeout     = 10 * time.Second
)

// progressReader records successful network reads without taking any lock
// needed by the sender or decoder. An idle incremental request may wait
// indefinitely; a partially received message must keep making progress.
type progressReader struct{ client *Client }

func (r progressReader) Read(p []byte) (int, error) {
	n, err := r.client.conn.Read(p)
	if n > 0 {
		r.client.lastRead.Store(time.Now().UnixNano())
	}
	return n, err
}

func (client *Client) fail(err error) {
	if err == nil {
		return
	}
	client.mu.Lock()
	if client.terminalErr == nil {
		client.terminalErr = err
	}
	client.mu.Unlock()
	_ = client.conn.Close()
}

// sendRequests owns update-request writes. Enqueuing is nonblocking: neither
// the image reader nor the watchdog may wait behind a blocked network write.
func (client *Client) sendRequests(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-client.requestWake:
		}
		client.mu.Lock()
		pending, full := client.requestPending, client.fullPending
		client.requestPending, client.fullPending = false, false
		client.mu.Unlock()
		if pending {
			if err := client.writeRequest(!full); err != nil {
				client.fail(err)
				return
			}
		}
	}
}

// noteInput also recognizes Ctrl+Left/Right. Some macOS screen-sharing servers
// stop answering incremental requests after a Space switch without sending a
// DesktopSize message. Repaint after the animation, then once more after it has
// settled; alternating switches move both deadlines to the latest shortcut.
func (client *Client) noteInput(keysym uint32, down bool) {
	now := time.Now()
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.inputPending.IsZero() {
		client.inputPending = now
	}
	var control uint8
	switch keysym {
	case 0xffe3: // Control_L
		control = 1
	case 0xffe4: // Control_R
		control = 2
	}
	if down {
		client.controlKeys |= control
	} else {
		client.controlKeys &^= control
	}
	if client.controlKeys != 0 && (keysym == 0xff51 || keysym == 0xff53) {
		client.refreshAt = now.Add(spaceRefreshDelay)
		client.refreshUntil = client.refreshAt.Add(spaceRefreshRetry)
	}
}

// recoverUpdates never polls an idle desktop. It sends a bounded full refresh
// after a Space shortcut, or when input has received no picture for a while.
// Repeated full requests are coalesced by request so a slow response keeps its
// original deadline and cannot accumulate an unbounded queue of full frames.
func (client *Client) recoverUpdates(now time.Time) error {
	client.mu.Lock()
	message, writing, receivingPixels := client.messageStarted, client.writeStarted, client.receivingPixels
	progress := time.Unix(0, client.lastRead.Load())
	if progress.Before(message) {
		progress = message
	}
	if !writing.IsZero() && now.Sub(writing) >= progressTimeout {
		client.mu.Unlock()
		return fmt.Errorf("%w: sending input or an update request", ErrStalled)
	}
	if !message.IsZero() && now.Sub(progress) >= progressTimeout {
		client.mu.Unlock()
		return fmt.Errorf("%w: receiving a server message", ErrStalled)
	}
	full := client.fullSince
	if full.IsZero() {
		full = client.fullRequested
	}
	if client.fullProgress.After(full) {
		full = client.fullProgress
	}
	if !full.IsZero() {
		client.mu.Unlock()
		if !receivingPixels && now.Sub(full) >= fullRequestTimeout {
			return fmt.Errorf("%w: waiting for the full picture", ErrStalled)
		}
		return nil
	}
	if !client.active {
		client.mu.Unlock()
		return nil
	}
	due := !client.refreshAt.IsZero() && !now.Before(client.refreshAt)
	silent := !client.inputPending.IsZero() && now.Sub(client.inputPending) >= inputRefreshDelay
	if !due && !silent {
		client.mu.Unlock()
		return nil
	}
	client.inputPending = time.Time{}
	if due {
		next := client.refreshAt.Add(spaceRefreshRetry)
		if !next.After(client.refreshUntil) {
			client.refreshAt = next
		} else {
			client.refreshAt = time.Time{}
		}
	}
	client.mu.Unlock()
	return client.request(false)
}

func (client *Client) watchUpdates(stop <-chan struct{}) {
	ticker := time.NewTicker(updateWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			if err := client.recoverUpdates(now); err != nil {
				client.fail(err)
				return
			}
		}
	}
}

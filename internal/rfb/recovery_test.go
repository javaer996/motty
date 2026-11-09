package rfb

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestAlternatingSpaceSwitchesRecoverWithoutLayoutMessages(t *testing.T) {
	clientSide, serverSide := pair(t)
	s := &server{t: t, conn: serverSide}
	go s.handshake("008", 1, "", 20, 10)
	client, err := Connect(clientSide, Config{})
	if err != nil {
		t.Fatal(err)
	}
	sink := &canvas{}
	done := run(client, sink)
	s.readRequest()
	s.write(update(rect(0, 0, 20, 10, EncodingRaw, pixels(200, 1, 2, 3)...)))
	s.readRequest()
	for change := 0; change < 8; change++ {
		sent := make(chan struct{})
		arrow := uint32(0xff51)
		if change%2 != 0 {
			arrow = 0xff53
		}
		go func() {
			defer close(sent)
			// Match the phone's key bar: Ctrl stays selected throughout
			// rapid alternating Left/Right taps.
			if change == 0 {
				client.SendKey(0xffe3, true)
			}
			client.SendKey(arrow, true)
			client.SendKey(arrow, false)
		}()
		keys := 2
		if change == 0 {
			keys++
		}
		s.read(keys * 8)
		<-sent
		// The server paints an animation frame, then loses incremental
		// damage tracking. There is no size/layout change to trigger recovery.
		s.write(update(rect(0, 0, 1, 1, EncodingRaw, pixels(1, 99, 0, 0)...)))
		if incremental, _, _ := s.readRequest(); !incremental {
			t.Fatal("animation frame did not request incremental updates")
		}
		if incremental, w, h := s.readRequest(); incremental || w != 20 || h != 10 {
			t.Fatalf("switch %d did not automatically request the full picture", change)
		}
		s.write(update(rect(0, 0, 20, 10, EncodingRaw, pixels(200, byte(change), 4, 5)...)))
		s.readRequest()
		if got := sink.at(0, 0); got != [3]byte{byte(change), 4, 5} {
			t.Fatalf("switch %d: stale pixel %v", change, got)
		}
	}
	// The second refresh also reaches the server after the animation settles.
	if incremental, _, _ := s.readRequest(); incremental {
		t.Fatal("missing refresh after the final animation settled")
	}
	s.finish()
	wait(t, done)
}

type recoveryConn struct {
	net.Conn
	writes [][]byte
	closed bool
}

func (c *recoveryConn) Write(p []byte) (int, error) {
	c.writes = append(c.writes, bytes.Clone(p))
	return len(p), nil
}

func (c *recoveryConn) Close() error { c.closed = true; return nil }

func TestUpdateRecoveryIsBoundedAndPreservesFullRequestDeadline(t *testing.T) {
	conn := &recoveryConn{}
	client := &Client{conn: conn, active: true, width: 20, height: 10}
	now := time.Now()
	client.noteInput(0, false)
	client.recoverUpdates(now.Add(inputRefreshDelay + time.Millisecond))
	if len(conn.writes) != 1 || conn.writes[0][1] != 0 {
		t.Fatal("unanswered input did not trigger a full request")
	}
	deadline := client.fullRequested
	client.Refresh()
	client.Refresh()
	client.recoverUpdates(deadline.Add(time.Second))
	if len(conn.writes) != 1 || client.fullRequested != deadline {
		t.Fatal("refreshes piled up or reset the existing response deadline")
	}
	err := client.recoverUpdates(deadline.Add(fullRequestTimeout))
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("unanswered full request did not time out: %v", err)
	}
	client.fail(err)
	if !conn.closed {
		t.Fatal("an unanswered full request never expired")
	}

	// An ordinary idle picture and a hidden desktop must not be polled.
	conn = &recoveryConn{}
	client = &Client{conn: conn, active: true}
	client.recoverUpdates(now.Add(time.Hour))
	client.active = false
	client.noteInput(0xffe3, true)
	client.noteInput(0xff51, true)
	client.recoverUpdates(now.Add(time.Hour))
	if len(conn.writes) != 0 {
		t.Fatal("an idle or hidden desktop was polled")
	}
}

func TestSlowFrameWithContinuingNetworkProgressDoesNotTimeOut(t *testing.T) {
	conn := &recoveryConn{}
	start := time.Now()
	client := &Client{conn: conn, active: true, fullRequested: start, messageStarted: start}
	client.receivingPixels = true
	now := start.Add(2 * fullRequestTimeout)
	client.lastRead.Store(now.Add(-time.Second).UnixNano())
	if err := client.recoverUpdates(now); err != nil {
		t.Fatalf("a slow frame still receiving bytes was disconnected: %v", err)
	}
	if len(conn.writes) != 0 {
		t.Fatal("a full frame already arriving was requested again")
	}
	if err := client.recoverUpdates(now.Add(progressTimeout)); !errors.Is(err, ErrStalled) {
		t.Fatalf("a frame that stopped receiving bytes did not time out: %v", err)
	}
}

func TestEmptyAndMetadataUpdatesDoNotAcknowledgeAFullPicture(t *testing.T) {
	for name, frame := range map[string][]byte{
		"empty": update(),
		"name":  update(rect(0, 0, 0, 0, encodingDesktopName, append(u32(4), []byte("desk")...)...)),
		"copy":  update(rect(0, 0, 1, 1, EncodingCopyRect, u16(0, 0)...)),
	} {
		t.Run(name, func(t *testing.T) {
			client := clientOver(frame[1:], 20, 10)
			sink := &canvas{}
			sink.Resize(20, 10)
			at := time.Now()
			client.fullRequested = at
			if err := client.update(sink); err != nil {
				t.Fatal(err)
			}
			if client.fullRequested.IsZero() {
				t.Fatal("an update without fresh pixels cancelled full-picture recovery")
			}
			// The server can keep transmitting metadata during every watchdog
			// tick. Network activity alone does not prove a picture is coming.
			client.messageStarted = at
			client.lastRead.Store(at.Add(fullRequestTimeout).UnixNano())
			if err := client.recoverUpdates(at.Add(fullRequestTimeout)); !errors.Is(err, ErrStalled) {
				t.Fatalf("a missing full picture was kept alive by %s updates: %v", name, err)
			}
		})
	}
}

type recoveryCheckReader func()

func (check recoveryCheckReader) Read([]byte) (int, error) {
	check()
	return 0, io.EOF
}

func TestMetadataAfterPixelsCannotKeepAFullPictureAlive(t *testing.T) {
	client := clientOver(nil, 20, 10)
	sink := &canvas{}
	sink.Resize(20, 10)
	client.Refresh()
	client.messageStarted = time.Now()
	// The frame starts with a small pixel update and then a desktop name
	// whose body is delayed. Recent metadata bytes cannot inherit the slow
	// pixel-frame allowance and keep the remaining picture missing forever.
	frame := update(
		rect(0, 0, 1, 1, EncodingRaw, pixels(1, 1, 2, 3)...),
		rect(0, 0, 0, 0, encodingDesktopName, u32(4)...),
	)
	checked := false
	client.r = bufio.NewReader(io.MultiReader(bytes.NewReader(frame[1:]), recoveryCheckReader(func() {
		checked = true
		now := time.Now().Add(fullRequestTimeout)
		client.lastRead.Store(now.UnixNano())
		if err := client.recoverUpdates(now); !errors.Is(err, ErrStalled) {
			t.Errorf("metadata after a pixel rectangle prevented full-picture recovery: %v", err)
		}
	})))
	if err := client.update(sink); !errors.Is(err, io.EOF) {
		t.Fatalf("expected the unfinished name to end the frame: %v", err)
	}
	if !checked {
		t.Fatal("the unfinished metadata body was not reached")
	}
}

func TestDuplicatePixelStreamCannotKeepAFullPictureAlive(t *testing.T) {
	client := clientOver(nil, 20, 10)
	sink := &canvas{}
	sink.Resize(20, 10)
	client.Refresh()
	frame := update(rect(0, 0, 1, 1, EncodingRaw, pixels(1, 1, 2, 3)...))
	client.r = bufio.NewReader(bytes.NewReader(frame[1:]))
	if err := client.update(sink); err != nil {
		t.Fatal(err)
	}
	client.messageStarted = time.Now()
	checked := false
	// A second transmission of the same pixel is receiving real image bytes,
	// but cannot fill any missing area of the full picture. Exercise the
	// watchdog while this repeated rectangle is still being decoded.
	client.r = bufio.NewReader(io.MultiReader(bytes.NewReader(frame[1:len(frame)-4]), recoveryCheckReader(func() {
		checked = true
		now := time.Now().Add(fullRequestTimeout)
		client.lastRead.Store(now.UnixNano())
		if err := client.recoverUpdates(now); !errors.Is(err, ErrStalled) {
			t.Errorf("repeated pixel bytes prevented full-picture recovery: %v", err)
		}
	})))
	if err := client.update(sink); !errors.Is(err, io.EOF) {
		t.Fatalf("expected the unfinished pixel body to end the frame: %v", err)
	}
	if !checked {
		t.Fatal("the unfinished pixel body was not reached")
	}
}

func TestLayoutRepliesCannotKeepMovingThePictureDeadline(t *testing.T) {
	frame := update(rect(0, 0, 20, 10, encodingExtendedDesktopSize, 0, 0, 0, 0))
	client := clientOver(frame[1:], 20, 10)
	sink := &canvas{}
	sink.Resize(20, 10)
	started := time.Now().Add(-fullRequestTimeout)
	client.fullRequested, client.fullSince = started, started
	if err := client.update(sink); err != nil {
		t.Fatal(err)
	}
	if client.fullSince != started {
		t.Fatal("a layout-only reply reset the original deadline")
	}
	if err := client.recoverUpdates(time.Now()); !errors.Is(err, ErrStalled) {
		t.Fatalf("repeated layouts prevented stream recovery: %v", err)
	}
}

type refreshDuringFrameSink struct {
	*canvas
	client *Client
}

func (s refreshDuringFrameSink) Update(x, y, width, height int, pix []byte, stride int) {
	s.canvas.Update(x, y, width, height, pix, stride)
	s.client.Refresh()
}

func TestFrameStartedBeforeRefreshCannotAcknowledgeIt(t *testing.T) {
	frame := update(rect(0, 0, 1, 1, EncodingRaw, pixels(1, 1, 2, 3)...))
	client := clientOver(frame[1:], 20, 10)
	image := &canvas{}
	image.Resize(20, 10)
	if err := client.update(refreshDuringFrameSink{image, client}); err != nil {
		t.Fatal(err)
	}
	if client.fullRequested.IsZero() || client.fullSince.IsZero() {
		t.Fatal("an older incremental frame incorrectly acknowledged a newer full request")
	}
}

func TestFullPictureRequiresCompleteCoverageAcrossUpdates(t *testing.T) {
	client := clientOver(nil, 67, 5) // rows and rectangles cross 64-bit boundaries
	sink := &canvas{}
	sink.Resize(67, 5)
	client.Refresh()
	paint := func(x, width int) {
		t.Helper()
		frame := update(rect(x, 0, width, 5, EncodingRaw, pixels(width*5, 1, 2, 3)...))
		client.r = bufio.NewReader(bytes.NewReader(frame[1:]))
		if err := client.update(sink); err != nil {
			t.Fatal(err)
		}
	}
	paint(0, 33)
	paint(31, 35) // overlaps, but still leaves the last column missing
	if client.fullRemaining != 5 || client.fullRequested.IsZero() {
		t.Fatalf("partial/overlapping rectangles acknowledged a full picture: %d pixels missing", client.fullRemaining)
	}
	progress := client.fullProgress
	for range 12 {
		paint(31, 35) // a busy cursor/dirty strip must not extend the deadline
	}
	client.fullRequested = time.Time{} // a layout-only reply causes a replacement request
	client.Refresh()
	paint(31, 35)
	if client.fullProgress != progress {
		t.Fatal("repeated pixels kept moving the recovery deadline")
	}
	if err := client.recoverUpdates(progress.Add(fullRequestTimeout)); !errors.Is(err, ErrStalled) {
		t.Fatalf("a full picture missing its last column never timed out: %v", err)
	}
	paint(66, 1)
	if !client.fullRequested.IsZero() || !client.fullSince.IsZero() {
		t.Fatal("a complete picture split across updates did not acknowledge the request")
	}
}

type clipboardAckSink struct {
	*canvas
	ack chan struct{}
}

func (s clipboardAckSink) ServerText(text string) {
	s.canvas.ServerText(text)
	close(s.ack)
}

func TestNonFramebufferMessagesDoNotAcknowledgeAFullRequest(t *testing.T) {
	clientSide, serverSide := pair(t)
	s := &server{t: t, conn: serverSide}
	go s.handshake("008", 1, "", 20, 10)
	client, err := Connect(clientSide, Config{})
	if err != nil {
		t.Fatal(err)
	}
	ack := make(chan struct{})
	done := run(client, clipboardAckSink{&canvas{}, ack})
	s.readRequest()
	s.write([]byte{2}, []byte{3, 0, 0, 0, 0, 0, 0, 1, 'x'})
	select {
	case <-ack:
	case <-time.After(time.Second):
		t.Fatal("clipboard was not delivered")
	}
	client.mu.Lock()
	at := client.fullRequested
	client.mu.Unlock()
	if at.IsZero() {
		t.Fatal("a bell or clipboard incorrectly acknowledged the full picture")
	}
	// Starting an update without finishing its pixels is not an answer either.
	partial := update(rect(0, 0, 1, 1, EncodingRaw, 1, 2, 3))
	s.write(partial)
	err = client.recoverUpdates(at.Add(fullRequestTimeout + time.Second))
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("partial full frame did not time out: %v", err)
	}
	client.fail(err)
	wait(t, done)
}

type blockedWriteConn struct {
	net.Conn
	mu      sync.Mutex
	armed   bool
	started chan struct{}
}

func (c *blockedWriteConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	if c.armed {
		c.armed = false
		close(c.started)
	}
	c.mu.Unlock()
	return c.Conn.Write(p)
}

func (c *blockedWriteConn) arm() {
	c.mu.Lock()
	c.armed = true
	c.mu.Unlock()
}

type bottomRowAckSink struct {
	*canvas
	painted    chan struct{}
	fullFrames int
}

func (s *bottomRowAckSink) Update(x, y, width, height int, pix []byte, stride int) {
	s.canvas.Update(x, y, width, height, pix, stride)
	if y+height == 256 {
		s.fullFrames++
		if s.fullFrames == 2 {
			close(s.painted)
		}
	}
}

func TestBlockedRefreshOrWheelNeverBlocksTheImageReader(t *testing.T) {
	for _, input := range []bool{false, true} {
		name := "refresh"
		if input {
			name = "wheel"
		}
		t.Run(name, func(t *testing.T) {
			clientSide, serverSide := pair(t)
			conn := &blockedWriteConn{Conn: clientSide, started: make(chan struct{})}
			s := &server{t: t, conn: serverSide}
			go s.handshake("008", 1, "", 256, 256)
			client, err := Connect(conn, Config{})
			if err != nil {
				t.Fatal(err)
			}
			sink := &bottomRowAckSink{canvas: &canvas{}, painted: make(chan struct{})}
			done := run(client, sink)
			s.readRequest()
			s.write(update(rect(0, 0, 256, 256, EncodingRaw, pixels(256*256, 1, 2, 3)...)))
			s.readRequest()
			conn.arm()
			sent := make(chan error, 1)
			go func() {
				if input {
					sent <- client.SendPointer(10, 10, 1<<4)
				} else {
					sent <- client.Refresh()
				}
			}()
			select {
			case <-conn.started:
			case <-time.After(time.Second):
				t.Fatal("sender did not begin")
			}
			// The server sends two frames before it reads any more input.
			// The first frame must not make the image reader wait for the
			// blocked sender, or both ends stop reading each other's writes.
			painted := make(chan error, 1)
			go func() {
				for _, frame := range [][]byte{
					update(rect(0, 0, 1, 1, EncodingRaw, pixels(1, 4, 5, 6)...)),
					update(rect(0, 0, 256, 256, EncodingRaw, pixels(256*256, 7, 8, 9)...)),
				} {
					if _, err := serverSide.Write(frame); err != nil {
						painted <- err
						return
					}
				}
				painted <- nil
			}()
			select {
			case err := <-painted:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				client.Close()
				<-painted
				t.Fatal("image reader deadlocked behind the blocked sender")
			}
			if input {
				s.read(6)
			} else if incremental, _, _ := s.readRequest(); incremental {
				t.Fatal("queued refresh was not a full request")
			}
			s.readRequest()
			if err := <-sent; err != nil {
				t.Fatal(err)
			}
			select {
			case <-sink.painted:
			case <-time.After(time.Second):
				t.Fatal("image reader did not finish painting")
			}
			if got := sink.at(255, 255); got != [3]byte{7, 8, 9} {
				t.Fatalf("new frame was not drawn: %v", got)
			}
			s.finish()
			wait(t, done)
		})
	}
}

func TestWatchdogRemainsAvailableWhenRequestWriteBlocks(t *testing.T) {
	clientSide, serverSide := pair(t)
	conn := &blockedWriteConn{Conn: clientSide, started: make(chan struct{})}
	s := &server{t: t, conn: serverSide}
	go s.handshake("008", 1, "", 20, 10)
	client, err := Connect(conn, Config{})
	if err != nil {
		t.Fatal(err)
	}
	done := run(client, &canvas{})
	s.readRequest()
	s.write(update(rect(0, 0, 20, 10, EncodingRaw, pixels(200, 1, 2, 3)...)))
	s.readRequest()
	conn.arm()
	client.Refresh()
	select {
	case <-conn.started:
	case <-time.After(time.Second):
		t.Fatal("refresh writer did not start")
	}
	client.mu.Lock()
	at := client.writeStarted
	client.mu.Unlock()
	err = client.recoverUpdates(at.Add(progressTimeout))
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("watchdog missed the blocked request: %v", err)
	}
	client.fail(err)
	if err := wait(t, done); !errors.Is(err, ErrStalled) {
		t.Fatalf("blocked connection did not report the recovery reason: %v", err)
	}
}

func TestPartialIncrementalFrameTimesOutWithoutAFullRequest(t *testing.T) {
	clientSide, serverSide := pair(t)
	s := &server{t: t, conn: serverSide}
	go s.handshake("008", 1, "", 20, 10)
	client, err := Connect(clientSide, Config{})
	if err != nil {
		t.Fatal(err)
	}
	done := run(client, &canvas{})
	s.readRequest()
	s.write(update(rect(0, 0, 20, 10, EncodingRaw, pixels(200, 1, 2, 3)...)))
	s.readRequest()
	s.write(update(rect(0, 0, 1, 1, EncodingRaw, 4, 5, 6))) // missing last pixel byte
	// Synchronize with the reader entering the partial message.
	for deadline := time.Now().Add(time.Second); ; {
		client.mu.Lock()
		started, full := client.messageStarted, client.fullRequested
		client.mu.Unlock()
		if !started.IsZero() {
			if !full.IsZero() {
				t.Fatal("test expected an incremental frame")
			}
			at := max(started.UnixNano(), client.lastRead.Load())
			err := client.recoverUpdates(time.Unix(0, at).Add(progressTimeout))
			if !errors.Is(err, ErrStalled) {
				t.Fatalf("partial incremental frame did not time out: %v", err)
			}
			client.fail(err)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reader did not enter partial message")
		}
		time.Sleep(time.Millisecond)
	}
	if err := wait(t, done); !errors.Is(err, ErrStalled) {
		t.Fatalf("partial frame did not release reader: %v", err)
	}
}

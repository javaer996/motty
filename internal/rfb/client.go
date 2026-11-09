package rfb

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// Limits on what a server may make the client allocate or wait for.
const (
	// MaxDimension is the largest width or height accepted, in pixels.
	MaxDimension = 16384
	// MaxPixels is the largest framebuffer accepted: 8K UHD, 133 MB of BGRA.
	MaxPixels = 7680 * 4320

	maxNameLength   = 4096
	maxReasonLength = 4096
	// MaxClipboard is the longest clipboard text taken from a server or sent to one.
	MaxClipboard = 256 << 10
	// maxCutText is the longest cut-text message the client reads and throws away
	// rather than closing the connection.
	maxCutText = 16 << 20
	// maxZRLE bounds the compressed bytes of one ZRLE rectangle beyond what the
	// rectangle's own size explains.
	maxZRLEExtra = 1 << 20
)

// Errors a caller may want to tell apart.
var (
	// ErrAuthRequired means the server asks for a password and none was given.
	ErrAuthRequired = errors.New("rfb: the server asks for a password")
	// ErrAuthFailed means the server refused the password.
	ErrAuthFailed = errors.New("rfb: authentication failed")
	// ErrUnsupported means the server offers nothing this client can speak.
	ErrUnsupported = errors.New("rfb: no supported security type")
	// ErrProtocol means the server broke the protocol or exceeded a limit.
	ErrProtocol = errors.New("rfb: protocol error")
)

// ServerError is a refusal the server explained in words.
type ServerError struct{ Reason string }

func (err *ServerError) Error() string {
	return "rfb: the server refused the connection: " + err.Reason
}

// Encoding numbers of RFC 6143 and the pseudo-encodings used here.
const (
	EncodingRaw         int32 = 0
	EncodingCopyRect    int32 = 1
	EncodingRRE         int32 = 2
	EncodingHextile     int32 = 5
	EncodingZRLE        int32 = 16
	encodingLastRect    int32 = -224
	encodingDesktopSize int32 = -223
	// Pseudo-encodings a server may send unasked when the monitor layout
	// changes (switching a macOS Space does this). They carry no pixels.
	encodingExtendedDesktopSize int32 = -308
	encodingDesktopName         int32 = -307
)

// DefaultEncodings is the preference order sent to the server: the most
// compact first, Raw last as what every server must support. Both desktop
// size pseudo-encodings are declared: the extended one, which carries the
// monitor layout, replaces the simple one for servers that speak it.
var DefaultEncodings = []int32{
	EncodingZRLE, EncodingHextile, EncodingRRE, EncodingCopyRect, EncodingRaw,
	encodingExtendedDesktopSize, encodingDesktopSize,
}

// Sink receives what the server draws. Calls come from the goroutine that runs
// Client.Run, in the order of the protocol. Pixels are blue, green, red and one
// unused byte, rows Stride bytes apart; pix is only valid during the call.
type Sink interface {
	// Resize is the framebuffer's size at the start and whenever the server
	// changes it. The content after it is undefined until updates arrive.
	Resize(width, height int)
	Update(x, y, width, height int, pix []byte, stride int)
	// Copy moves a rectangle already on the framebuffer; source and destination
	// may overlap.
	Copy(x, y, width, height, fromX, fromY int)
	Fill(x, y, width, height int, blue, green, red byte)
	Bell()
	// ServerText is the server's clipboard, as UTF-8, at most MaxClipboard bytes.
	ServerText(text string)
}

// Config describes how to connect.
type Config struct {
	// Password is the VNC password. Empty means none: a server that asks for one
	// then fails the connection with ErrAuthRequired.
	Password string
	// AskPassword, when set, is called during the handshake, on the connecting
	// goroutine, if the server wants a password and Password is empty: it may block
	// while the user types it (the handshake's timeout is paused meanwhile) and the
	// same connection goes on with the answer, as a viewer's password dialog does.
	// Returning an error ends the connection with it.
	AskPassword func() (string, error)
	// Exclusive asks the server to disconnect other clients. The default is a
	// shared session, so connecting does not throw out whoever is already there.
	Exclusive bool
	// Encodings overrides DefaultEncodings, in order of preference.
	Encodings []int32
	// Timeout bounds the handshake. Zero means 20 seconds.
	Timeout time.Duration
	// MinRequestInterval slows the rate at which updates are asked for: a
	// server sends a frame only when asked, so this caps how much it can send
	// and how much bandwidth a slow link has to carry. Zero asks as fast as
	// each update arrives.
	MinRequestInterval time.Duration
}

// Client is one connection to a server. Methods other than Run may be called from
// any goroutine once Connect has returned.
type Client struct {
	conn net.Conn
	r    *bufio.Reader

	// Name is the desktop's name the server reported, with control characters
	// removed.
	Name string
	// Version is the protocol minor version used: 3, 7 or 8.
	Version int
	// Security is the security type used: 1 (None) or 2 (VNC Authentication).
	Security int

	width, height int
	timeout       time.Duration

	writeMu sync.Mutex

	mu      sync.Mutex
	active  bool // the user can see the desktop: ask for updates
	waiting bool // an update arrived while inactive, so none is asked for yet
	running bool
	// minInterval and lastRequest slow down what is asked for; kept by mu.
	minInterval time.Duration
	lastRequest time.Time
	// fullRequested is when a full-update request went out and no answer has
	// started to arrive; kept by mu. A server must answer a full request, so
	// one that stays unanswered means the stream is out of step or the server
	// is wedged: no further picture will ever come.
	fullRequested time.Time

	// decoder state
	rowBuf  []byte
	tileBuf []byte
	zrle    zrleState
}

// Connect speaks the handshake over conn and returns a client ready to Run. On
// error the caller closes conn. The connection stays the caller's to close in
// all cases; Close also does.
func Connect(conn net.Conn, config Config) (*Client, error) {
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	defer conn.SetDeadline(time.Time{})
	client := &Client{
		conn:        conn,
		r:           bufio.NewReaderSize(conn, 64<<10),
		active:      true,
		timeout:     timeout,
		minInterval: config.MinRequestInterval,
	}
	if err := client.handshake(config); err != nil {
		return nil, err
	}
	if err := client.init(config); err != nil {
		return nil, err
	}
	return client, nil
}

// Size is the framebuffer's size at the time of the handshake.
func (client *Client) Size() (width, height int) { return client.width, client.height }

func (client *Client) handshake(config Config) error {
	var banner [12]byte
	if _, err := io.ReadFull(client.r, banner[:]); err != nil {
		return fmt.Errorf("rfb: reading the server's version: %w", err)
	}
	major, minor, ok := parseVersion(banner[:])
	if !ok {
		return fmt.Errorf("%w: not an RFB server (%q)", ErrProtocol, printable(banner[:]))
	}
	if major != 3 {
		return fmt.Errorf("%w: protocol version %d.%d", ErrUnsupported, major, minor)
	}
	switch {
	case minor >= 8:
		client.Version = 8
	case minor == 7:
		client.Version = 7
	default:
		client.Version = 3
	}
	if _, err := fmt.Fprintf(client.conn, "RFB 003.%03d\n", client.Version); err != nil {
		return err
	}
	var security uint8
	switch client.Version {
	case 3:
		var value uint32
		if err := binary.Read(client.r, binary.BigEndian, &value); err != nil {
			return err
		}
		switch value {
		case 0:
			return client.failure()
		case 1, 2:
			security = uint8(value)
		default:
			return fmt.Errorf("%w: security type %d", ErrUnsupported, value)
		}
	default:
		count, err := client.r.ReadByte()
		if err != nil {
			return err
		}
		if count == 0 {
			return client.failure()
		}
		types := make([]byte, count)
		if _, err := io.ReadFull(client.r, types); err != nil {
			return err
		}
		security = chooseSecurity(types, config.Password != "" || config.AskPassword != nil)
		if security == 0 {
			return fmt.Errorf("%w: the server offers %v", ErrUnsupported, types)
		}
		if _, err := client.conn.Write([]byte{security}); err != nil {
			return err
		}
	}
	client.Security = int(security)
	switch security {
	case 1:
		if client.Version == 8 {
			return client.securityResult()
		}
		return nil
	case 2:
		if config.Password == "" && config.AskPassword == nil {
			return ErrAuthRequired
		}
		var challenge [16]byte
		if _, err := io.ReadFull(client.r, challenge[:]); err != nil {
			return err
		}
		password := config.Password
		if password == "" {
			_ = client.conn.SetDeadline(time.Time{}) // the user's time is not the network's
			asked, err := config.AskPassword()
			if err != nil {
				return err
			}
			if asked == "" {
				return ErrAuthRequired
			}
			password = asked
			_ = client.conn.SetDeadline(time.Now().Add(client.timeout))
		}
		response := vncResponse(challenge, password)
		if _, err := client.conn.Write(response[:]); err != nil {
			return err
		}
		return client.securityResult()
	}
	return fmt.Errorf("%w: security type %d", ErrUnsupported, security)
}

// chooseSecurity picks from what the server offers: VNC Authentication when a
// password is at hand and offered, otherwise None, otherwise VNC
// Authentication to ask for the password.
func chooseSecurity(offered []byte, havePassword bool) uint8 {
	has := func(want byte) bool {
		for _, value := range offered {
			if value == want {
				return true
			}
		}
		return false
	}
	switch {
	case havePassword && has(2):
		return 2
	case has(1):
		return 1
	case has(2):
		return 2
	}
	return 0
}

func (client *Client) securityResult() error {
	var result uint32
	if err := binary.Read(client.r, binary.BigEndian, &result); err != nil {
		return err
	}
	if result == 0 {
		return nil
	}
	if client.Version == 8 {
		if err := client.failure(); err != nil {
			var refusal *ServerError
			if errors.As(err, &refusal) {
				return fmt.Errorf("%w: %s", ErrAuthFailed, refusal.Reason)
			}
			// Some servers drop the connection instead of describing the
			// refusal. The refusal still happened, and the front end must ask
			// for another password rather than treat it as a network fault.
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return ErrAuthFailed
			}
			return err
		}
	}
	return ErrAuthFailed
}

// failure reads a reason string the server sends with a refusal.
func (client *Client) failure() error {
	var length uint32
	if err := binary.Read(client.r, binary.BigEndian, &length); err != nil {
		return err
	}
	if length > maxReasonLength {
		return fmt.Errorf("%w: the refusal's reason is %d bytes", ErrProtocol, length)
	}
	reason := make([]byte, length)
	if _, err := io.ReadFull(client.r, reason); err != nil {
		return err
	}
	return &ServerError{Reason: printable(reason)}
}

// init sends ClientInit, reads ServerInit and tells the server the pixel format
// and encodings wanted.
func (client *Client) init(config Config) error {
	shared := byte(1)
	if config.Exclusive {
		shared = 0
	}
	if _, err := client.conn.Write([]byte{shared}); err != nil {
		return err
	}
	var header [24]byte
	if _, err := io.ReadFull(client.r, header[:]); err != nil {
		return fmt.Errorf("rfb: reading the server's description: %w", err)
	}
	width, height := int(binary.BigEndian.Uint16(header[0:])), int(binary.BigEndian.Uint16(header[2:]))
	if err := checkSize(width, height); err != nil {
		return err
	}
	client.width, client.height = width, height
	nameLength := binary.BigEndian.Uint32(header[20:])
	if nameLength > maxNameLength {
		return fmt.Errorf("%w: the desktop's name is %d bytes", ErrProtocol, nameLength)
	}
	name := make([]byte, nameLength)
	if _, err := io.ReadFull(client.r, name); err != nil {
		return err
	}
	client.Name = printable(name)

	encodings := config.Encodings
	if encodings == nil {
		encodings = DefaultEncodings
	}
	message := make([]byte, 0, 20+4+4*len(encodings))
	// SetPixelFormat: 32 bits per pixel, depth 24, little-endian true colour,
	// red, green and blue in bits 16, 8 and 0: memory order blue, green, red, pad.
	message = append(message, 0, 0, 0, 0, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0)
	message = append(message, 2, 0)
	message = binary.BigEndian.AppendUint16(message, uint16(len(encodings)))
	for _, encoding := range encodings {
		message = binary.BigEndian.AppendUint32(message, uint32(encoding))
	}
	return client.write(message)
}

func checkSize(width, height int) error {
	if width > MaxDimension || height > MaxDimension || width*height > MaxPixels {
		return fmt.Errorf("%w: a %dx%d desktop is larger than the %d pixels accepted", ErrProtocol, width, height, MaxPixels)
	}
	return nil
}

// parseVersion reads "RFB xxx.yyy\n".
func parseVersion(banner []byte) (major, minor int, ok bool) {
	if string(banner[:4]) != "RFB " || banner[7] != '.' || banner[11] != '\n' {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(string(banner[4:7]))
	minor, err2 := strconv.Atoi(string(banner[8:11]))
	return major, minor, err1 == nil && err2 == nil
}

// printable keeps what is safe to show: control characters and invalid UTF-8
// become nothing.
func printable(data []byte) string {
	out := make([]rune, 0, len(data))
	for _, r := range string(data) {
		if r >= ' ' && r != 0x7f && r != 0xfffd {
			out = append(out, r)
		}
	}
	return string(out)
}

func (client *Client) write(message []byte) error {
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	_, err := client.conn.Write(message)
	return err
}

// Close closes the connection, which ends Run.
func (client *Client) Close() error { return client.conn.Close() }

// SetActive says whether anyone can see the desktop. While it is false the
// client asks for no more updates, so a hidden tab costs the server and the
// network nothing; when it becomes true the next request goes out at once.
func (client *Client) SetActive(active bool) {
	client.mu.Lock()
	client.active = active
	resume := active && client.waiting && client.running
	client.waiting = false
	client.mu.Unlock()
	if resume {
		_ = client.request(true)
	}
}

// request asks for the desktop to be sent: only what changed when incremental.
func (client *Client) request(incremental bool) error {
	client.mu.Lock()
	width, height := client.width, client.height
	var wait time.Duration
	if client.minInterval > 0 {
		wait = time.Until(client.lastRequest.Add(client.minInterval))
		if wait > 0 {
			client.lastRequest = client.lastRequest.Add(client.minInterval)
		} else {
			client.lastRequest = time.Now()
			wait = 0
		}
	}
	client.mu.Unlock()
	if wait > 0 {
		time.Sleep(wait)
	}
	var flag byte
	if incremental {
		flag = 1
	}
	message := [10]byte{3, flag}
	binary.BigEndian.PutUint16(message[6:], uint16(width))
	binary.BigEndian.PutUint16(message[8:], uint16(height))
	if err := client.write(message[:]); err != nil {
		return err
	}
	if !incremental {
		client.mu.Lock()
		client.fullRequested = time.Now()
		client.mu.Unlock()
	}
	return nil
}

// Refresh asks the server for the whole picture, not only what changed. A
// server that stopped sending updates after its screen layout changed starts
// again on this request.
func (client *Client) Refresh() error { return client.request(false) }

// fullRequestTimeout is how long a full-update request may go unanswered
// before the connection is considered dead. A server must answer one, so a
// silent one is out of step and can never paint again; closing it lets the
// caller dial again and recover the picture.
const fullRequestTimeout = 10 * time.Second

// watchFullRequests closes the connection when a full request stays
// unanswered. It runs until stop is closed or it closes the connection itself.
func (client *Client) watchFullRequests(stop <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			client.mu.Lock()
			at := client.fullRequested
			client.mu.Unlock()
			if !at.IsZero() && time.Since(at) >= fullRequestTimeout {
				_ = client.conn.Close()
				return
			}
		}
	}
}

// SendPointer reports the pointer at (x, y) of the framebuffer with the buttons
// held: bit 0 left, 1 middle, 2 right, 3 and 4 wheel up and down, 5 and 6 wheel
// left and right.
func (client *Client) SendPointer(x, y int, buttons uint8) error {
	var message [6]byte
	message[0] = 5
	message[1] = buttons
	binary.BigEndian.PutUint16(message[2:], uint16(clamp(x, 0, 65535)))
	binary.BigEndian.PutUint16(message[4:], uint16(clamp(y, 0, 65535)))
	return client.write(message[:])
}

// SendKey presses or releases the key with this X11 keysym.
func (client *Client) SendKey(keysym uint32, down bool) error {
	var message [8]byte
	message[0] = 4
	if down {
		message[1] = 1
	}
	binary.BigEndian.PutUint32(message[4:], keysym)
	return client.write(message[:])
}

// SendClipboard gives the server text for its clipboard. The protocol carries
// Latin-1 only: other characters are sent as '?'. Text beyond MaxClipboard bytes
// is cut.
func (client *Client) SendClipboard(text string) error {
	data := make([]byte, 0, min(len(text), MaxClipboard))
	for _, r := range text {
		if len(data) >= MaxClipboard {
			break
		}
		switch {
		case r == '\r':
			continue
		case r < 256:
			data = append(data, byte(r))
		default:
			data = append(data, '?')
		}
	}
	message := make([]byte, 8, 8+len(data))
	message[0] = 6
	binary.BigEndian.PutUint32(message[4:], uint32(len(data)))
	return client.write(append(message, data...))
}

func clamp(value, low, high int) int { return max(low, min(value, high)) }

// Run reads server messages and delivers them to sink until the connection ends
// or the server breaks the protocol, and returns why. It asks for the first
// update, and for the next as each is applied, never in a loop of its own.
func (client *Client) Run(sink Sink) error {
	sink.Resize(client.width, client.height)
	client.mu.Lock()
	client.running = true
	client.mu.Unlock()
	stop := make(chan struct{})
	defer close(stop)
	go client.watchFullRequests(stop)
	if err := client.request(false); err != nil {
		return err
	}
	for {
		kind, err := client.r.ReadByte()
		if err != nil {
			return err
		}
		// An answer has started: whatever was last asked for is being served.
		client.mu.Lock()
		client.fullRequested = time.Time{}
		client.mu.Unlock()
		switch kind {
		case 0:
			err = client.update(sink)
		case 1: // SetColourMapEntries: the format is true colour, so there is no map
			var header [5]byte
			if _, err = io.ReadFull(client.r, header[:]); err == nil {
				_, err = io.CopyN(io.Discard, client.r, 6*int64(binary.BigEndian.Uint16(header[3:])))
			}
		case 2:
			sink.Bell()
		case 3:
			err = client.cutText(sink)
		default:
			err = fmt.Errorf("%w: message type %d", ErrProtocol, kind)
		}
		if err != nil {
			return err
		}
	}
}

func (client *Client) cutText(sink Sink) error {
	var header [7]byte
	if _, err := io.ReadFull(client.r, header[:]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(header[3:])
	if length > maxCutText {
		return fmt.Errorf("%w: a %d byte clipboard", ErrProtocol, length)
	}
	if length > MaxClipboard {
		_, err := io.CopyN(io.Discard, client.r, int64(length))
		return err
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(client.r, data); err != nil {
		return err
	}
	text := make([]rune, 0, len(data))
	for _, value := range data {
		if value != '\r' {
			text = append(text, rune(value))
		}
	}
	sink.ServerText(string(text))
	return nil
}

// update reads one FramebufferUpdate and then, if anyone is looking, asks for the
// next.
func (client *Client) update(sink Sink) error {
	var header [3]byte
	if _, err := io.ReadFull(client.r, header[:]); err != nil {
		return err
	}
	count := int(binary.BigEndian.Uint16(header[1:]))
	resized := false
	for index := 0; index < count; index++ {
		var rect [12]byte
		if _, err := io.ReadFull(client.r, rect[:]); err != nil {
			return err
		}
		x, y := int(binary.BigEndian.Uint16(rect[0:])), int(binary.BigEndian.Uint16(rect[2:]))
		width, height := int(binary.BigEndian.Uint16(rect[4:])), int(binary.BigEndian.Uint16(rect[6:]))
		encoding := int32(binary.BigEndian.Uint32(rect[8:]))
		switch encoding {
		case encodingLastRect:
			index = count
			continue
		case encodingDesktopSize:
			// A new framebuffer size. Whatever the old picture held is
			// doubted, so the server is asked for the whole picture again:
			// servers differ in what they repaint after a size change, and an
			// incremental request can leave one that waits for a full one
			// hanging forever.
			if err := checkSize(width, height); err != nil {
				return err
			}
			client.mu.Lock()
			changed := width != client.width || height != client.height
			client.width, client.height = width, height
			client.mu.Unlock()
			if changed {
				sink.Resize(width, height)
			}
			resized = true
			continue
		case encodingExtendedDesktopSize:
			// The rectangle header holds the new framebuffer size, and its x
			// and y the reason and status of a layout change; the payload is
			// the layout itself and carries no pixels. The server may treat
			// any layout change as making the picture stale, so a full
			// request follows even when the size did not change; Frame.Resize
			// keeps the picture when the size is the same, so nothing is
			// thrown away.
			if err := client.skipExtendedDesktopSize(); err != nil {
				return err
			}
			if width <= 0 || height <= 0 {
				continue
			}
			if err := checkSize(width, height); err != nil {
				return err
			}
			client.mu.Lock()
			changed := width != client.width || height != client.height
			client.width, client.height = width, height
			client.mu.Unlock()
			if changed {
				sink.Resize(width, height)
			}
			resized = true
			continue
		case encodingDesktopName:
			// A new name, not a new picture: the name is already known from
			// ServerInit, so the payload is only skipped.
			var length [4]byte
			if _, err := io.ReadFull(client.r, length[:]); err != nil {
				return err
			}
			size := binary.BigEndian.Uint32(length[:])
			if size > maxNameLength {
				return fmt.Errorf("%w: the desktop's name is %d bytes", ErrProtocol, size)
			}
			if _, err := io.CopyN(io.Discard, client.r, int64(size)); err != nil {
				return err
			}
			continue
		}
		if x+width > client.width || y+height > client.height {
			return fmt.Errorf("%w: a %dx%d rectangle at %d,%d outside the %dx%d desktop", ErrProtocol, width, height, x, y, client.width, client.height)
		}
		var err error
		switch encoding {
		case EncodingRaw:
			err = client.decodeRaw(sink, x, y, width, height)
		case EncodingCopyRect:
			err = client.decodeCopyRect(sink, x, y, width, height)
		case EncodingRRE:
			err = client.decodeRRE(sink, x, y, width, height)
		case EncodingHextile:
			err = client.decodeHextile(sink, x, y, width, height)
		case EncodingZRLE:
			err = client.decodeZRLE(sink, x, y, width, height)
		default:
			err = fmt.Errorf("%w: encoding %d", ErrProtocol, encoding)
		}
		if err != nil {
			return err
		}
	}
	client.mu.Lock()
	active := client.active
	client.waiting = !active
	client.mu.Unlock()
	if resized {
		return client.request(false)
	}
	if active {
		return client.request(true)
	}
	return nil
}

// skipExtendedDesktopSize reads and discards the payload of an
// ExtendedDesktopSize pseudo-rectangle (the RFB community extension, -308):
// the server's size limits, then one entry per screen:
//
//	U16 x-min, U16 y-min, U16 x-max, U16 y-max
//	U8 number-of-screens, 3 bytes padding
//	number-of-screens × { U32 id, U16 x, U16 y, U16 width, U16 height, U32 flags }
//
// The new framebuffer size is not in here: it is in the rectangle header,
// which the caller has already read. A simple viewer only needs to skip the
// layout.
func (client *Client) skipExtendedDesktopSize() error {
	var header [12]byte
	if _, err := io.ReadFull(client.r, header[:]); err != nil {
		return err
	}
	count := int(header[8])
	for range count {
		var screen [16]byte
		if _, err := io.ReadFull(client.r, screen[:]); err != nil {
			return err
		}
	}
	return nil
}

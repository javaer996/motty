//go:build !ios

package mobilecore

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func startLocalTestApp(t *testing.T, enabled bool) *App {
	t.Helper()
	app, err := Start(Config{DataDir: t.TempDir(), Version: "test", LocalShell: enabled})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(app.Close)
	return app
}

func openLocalTestSession(t *testing.T, app *App, cols, rows int) SessionInfo {
	t.Helper()
	params, _ := json.Marshal(map[string]int{"cols": cols, "rows": rows})
	envelope := callApp(t, app, "sessions.openLocal", string(params))
	if !envelope.OK {
		t.Fatalf("sessions.openLocal: %+v", envelope.Error)
	}
	var info SessionInfo
	if err := json.Unmarshal(envelope.Result, &info); err != nil {
		t.Fatalf("decode SessionInfo: %v", err)
	}
	return info
}

func localSessionInfo(t *testing.T, app *App, id uint32) (SessionInfo, bool) {
	t.Helper()
	envelope := callApp(t, app, "sessions.list", "")
	if !envelope.OK {
		t.Fatalf("sessions.list: %+v", envelope.Error)
	}
	var payload struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal(envelope.Result, &payload); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	for _, info := range payload.Sessions {
		if info.ID == id {
			return info, true
		}
	}
	return SessionInfo{}, false
}

func waitLocalPhase(t *testing.T, app *App, id uint32, want string) SessionInfo {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last SessionInfo
	for time.Now().Before(deadline) {
		info, ok := localSessionInfo(t, app, id)
		if ok {
			last = info
			if info.Phase == want {
				return info
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %d phase = %q, want %q (last: %+v)", id, last.Phase, want, last)
	return SessionInfo{}
}

func sendLocalInput(t *testing.T, app *App, id uint32, text string) {
	t.Helper()
	params := fmt.Sprintf(`{"id":%d,"data":%q}`, id, base64.StdEncoding.EncodeToString([]byte(text)))
	envelope := callApp(t, app, "sessions.input", params)
	if !envelope.OK {
		t.Fatalf("sessions.input: %+v", envelope.Error)
	}
}

// screenText decodes a snapshot into text, one line per row.
func screenText(t *testing.T, app *App, id uint32) string {
	t.Helper()
	buffer := make([]byte, 1<<20)
	n := app.ScreenSnapshot(id, 0, buffer)
	if n < 0 {
		return ""
	}
	if n == 0 {
		return ""
	}
	rows := int(binary.LittleEndian.Uint32(buffer[12:]))
	offset := screenHeaderSize
	var text strings.Builder
	for y := 0; y < rows; y++ {
		count := int(binary.LittleEndian.Uint32(buffer[offset:]))
		offset += 4
		for c := 0; c < count; c++ {
			r := rune(binary.LittleEndian.Uint32(buffer[offset:]))
			offset += screenCellSize
			if r == 0 {
				text.WriteByte(' ')
			} else {
				text.WriteRune(r)
			}
		}
		text.WriteByte('\n')
	}
	return text.String()
}

func waitForScreenText(t *testing.T, app *App, id uint32, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		last = screenText(t, app, id)
		if strings.Contains(last, want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("screen never showed %q; last screen:\n%s", want, last)
}

func TestLocalSessionStartsAndEchoes(t *testing.T) {
	app := startLocalTestApp(t, true)
	info := openLocalTestSession(t, app, 40, 10)
	if !info.Local || info.HostID != "" {
		t.Fatalf("openLocal returned %+v, want a local session", info)
	}
	waitLocalPhase(t, app, info.ID, "connected")

	sendLocalInput(t, app, info.ID, "echo motty-local\r")
	waitForScreenText(t, app, info.ID, "motty-local")

	closed := callApp(t, app, "sessions.close", fmt.Sprintf(`{"id":%d}`, info.ID))
	if !closed.OK {
		t.Fatalf("sessions.close: %+v", closed.Error)
	}
	if _, ok := localSessionInfo(t, app, info.ID); ok {
		t.Fatal("the session is still listed after close")
	}
}

func TestLocalResize(t *testing.T) {
	app := startLocalTestApp(t, true)
	info := openLocalTestSession(t, app, 40, 10)
	waitLocalPhase(t, app, info.ID, "connected")

	resized := callApp(t, app, "sessions.resize", fmt.Sprintf(`{"id":%d,"cols":50,"rows":12}`, info.ID))
	if !resized.OK {
		t.Fatalf("sessions.resize: %+v", resized.Error)
	}
	updated, ok := localSessionInfo(t, app, info.ID)
	if !ok || updated.Cols != 50 || updated.Rows != 12 {
		t.Fatalf("session after resize = %+v", updated)
	}

	snapshot := make([]byte, 1<<20)
	n := app.ScreenSnapshot(info.ID, 0, snapshot)
	if n <= 0 {
		t.Fatalf("snapshot after resize returned %d", n)
	}
	if got := binary.LittleEndian.Uint32(snapshot[8:]); got != 50 {
		t.Fatalf("snapshot cols = %d, want 50", got)
	}
	if got := binary.LittleEndian.Uint32(snapshot[12:]); got != 12 {
		t.Fatalf("snapshot rows = %d, want 12", got)
	}
}

func TestLocalReconnect(t *testing.T) {
	app := startLocalTestApp(t, true)
	info := openLocalTestSession(t, app, 40, 10)
	waitLocalPhase(t, app, info.ID, "connected")

	sendLocalInput(t, app, info.ID, "exit\r")
	waitLocalPhase(t, app, info.ID, "closed")

	reconnected := callApp(t, app, "sessions.reconnect", fmt.Sprintf(`{"id":%d}`, info.ID))
	if !reconnected.OK {
		t.Fatalf("sessions.reconnect: %+v", reconnected.Error)
	}
	waitLocalPhase(t, app, info.ID, "connected")

	sendLocalInput(t, app, info.ID, "echo motty-again\r")
	waitForScreenText(t, app, info.ID, "motty-again")
}

func TestLocalDisabled(t *testing.T) {
	app := startLocalTestApp(t, false)
	envelope := callApp(t, app, "sessions.openLocal", `{"cols":40,"rows":10}`)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != CodeUnavailable {
		t.Fatalf("openLocal without support returned %+v", envelope)
	}
}

func TestLocalDrainsOutputBeforeClosing(t *testing.T) {
	app := startLocalTestApp(t, true)
	info := openLocalTestSession(t, app, 40, 10)
	waitLocalPhase(t, app, info.ID, "connected")

	sendLocalInput(t, app, info.ID, "echo motty-last; exit\r")
	waitLocalPhase(t, app, info.ID, "closed")
	waitForScreenText(t, app, info.ID, "motty-last")
}

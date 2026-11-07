package mobilecore

import (
	"encoding/binary"
	"testing"

	"motty/internal/term"
)

func TestEncodeScreenLayout(t *testing.T) {
	terminal := term.New(10, 3, 100)
	if _, err := terminal.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}

	want := screenHeaderSize + 3*(4+10*screenCellSize)
	if got := snapshotSize(terminal); got != want {
		t.Fatalf("snapshotSize = %d, want %d", got, want)
	}
	if got := encodeScreen(terminal, nil); got != -want {
		t.Fatalf("encodeScreen(nil) = %d, want %d", got, -want)
	}

	buffer := make([]byte, want)
	n := encodeScreen(terminal, buffer)
	if n != want {
		t.Fatalf("encodeScreen = %d, want %d", n, want)
	}
	if got := binary.LittleEndian.Uint64(buffer[0:]); got != terminal.Version() {
		t.Fatalf("version = %d, want %d", got, terminal.Version())
	}
	if got := binary.LittleEndian.Uint32(buffer[8:]); got != 10 {
		t.Fatalf("cols = %d, want 10", got)
	}
	if got := binary.LittleEndian.Uint32(buffer[12:]); got != 3 {
		t.Fatalf("rows = %d, want 3", got)
	}
	if got := binary.LittleEndian.Uint32(buffer[16:]); got != 5 {
		t.Fatalf("cursorX = %d, want 5", got)
	}
	if got := binary.LittleEndian.Uint32(buffer[20:]); got != 0 {
		t.Fatalf("cursorY = %d, want 0", got)
	}
	if flags := binary.LittleEndian.Uint32(buffer[28:]); flags&screenFlagCursorVisible == 0 {
		t.Fatalf("flags = %#x, want the cursor-visible bit", flags)
	}

	count := binary.LittleEndian.Uint32(buffer[screenHeaderSize:])
	if count != 10 {
		t.Fatalf("first row has %d cells, want 10", count)
	}
	first := screenHeaderSize + 4
	if got := rune(binary.LittleEndian.Uint32(buffer[first:])); got != 'h' {
		t.Fatalf("first cell rune = %q, want 'h'", got)
	}
}

func TestEncodeScreenWideCell(t *testing.T) {
	terminal := term.New(6, 2, 0)
	if _, err := terminal.Write([]byte("界")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, snapshotSize(terminal))
	if n := encodeScreen(terminal, buffer); n != len(buffer) {
		t.Fatalf("encodeScreen = %d, want %d", n, len(buffer))
	}
	first := screenHeaderSize + 4
	if got := rune(binary.LittleEndian.Uint32(buffer[first:])); got != '界' {
		t.Fatalf("head rune = %q, want '界'", got)
	}
	if got := buffer[first+14]; got != 2 {
		t.Fatalf("head width = %d, want 2", got)
	}
	if got := buffer[first+15]; got != 1 {
		t.Fatalf("head flags = %d, want 1", got)
	}
	tail := first + screenCellSize
	if got := buffer[tail+15]; got != 2 {
		t.Fatalf("tail flags = %d, want 2", got)
	}
}

func TestEncodeScreenRejectsShortBuffer(t *testing.T) {
	terminal := term.New(8, 4, 0)
	need := snapshotSize(terminal)
	if got := encodeScreen(terminal, make([]byte, need-1)); got != -need {
		t.Fatalf("encodeScreen(short) = %d, want %d", got, -need)
	}
}

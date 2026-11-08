package mobilecore

import (
	"encoding/binary"

	"motty/internal/term"
)

// Screen snapshot layout, little-endian. It is deliberately dumb: the front
// end decodes positions and draws cells, and all terminal semantics stay in
// term.Terminal.
//
//	header, 48 bytes:
//	  u64 version
//	  u32 cols, rows
//	  u32 cursorX, cursorY
//	  u32 cursorStyle, flags
//	  u32 scrollback, viewOffset
//	  u32 defaultFg, defaultBg
//	  u32 reserved0, reserved1
//	then rows times:
//	  u32 cellCount
//	  cellCount × 16 bytes:
//	    u32 rune
//	    u32 fg     term.Color: kind<<24 | value
//	    u32 bg
//	    u16 attr
//	    u8  width  1 or 2
//	    u8  flags  bit0 wide head, bit1 wide tail
const (
	screenHeaderSize = 48
	screenCellSize   = 16

	screenFlagAltScreen      = 1 << 0
	screenFlagCursorVisible  = 1 << 1
	screenFlagCursorSteady   = 1 << 2
	screenFlagBracketedPaste = 1 << 3
	screenFlagAppCursorKeys  = 1 << 4
	screenFlagReverseVideo   = 1 << 5
	screenFlagMouse          = 1 << 6
	screenFlagSync           = 1 << 7
)

// snapshotSize reports how many bytes encodeScreen would write.
func snapshotSize(t *term.Terminal) int {
	_, rows := t.Size()
	size := screenHeaderSize
	for y := 0; y < rows; y++ {
		size += 4 + len(t.Row(y).Cells)*screenCellSize
	}
	return size
}

// encodeScreen writes the snapshot into dst. It returns the number of bytes
// written, or -needed when dst is too small (including when it is empty), so
// a caller can size its buffer without a second query.
func encodeScreen(t *term.Terminal, dst []byte) int {
	need := snapshotSize(t)
	if len(dst) < need {
		return -need
	}
	cols, rows := t.Size()
	cursor := t.Cursor()
	modes := t.Modes()

	flags := uint32(0)
	if modes.AltScreen {
		flags |= screenFlagAltScreen
	}
	if cursor.Visible {
		flags |= screenFlagCursorVisible
	}
	if cursor.Steady {
		flags |= screenFlagCursorSteady
	}
	if modes.BracketedPaste {
		flags |= screenFlagBracketedPaste
	}
	if modes.AppCursorKeys {
		flags |= screenFlagAppCursorKeys
	}
	if modes.ReverseVideo {
		flags |= screenFlagReverseVideo
	}
	if modes.Mouse != term.MouseOff {
		flags |= screenFlagMouse
	}
	if modes.Sync {
		flags |= screenFlagSync
	}

	binary.LittleEndian.PutUint64(dst[0:], t.Version())
	binary.LittleEndian.PutUint32(dst[8:], uint32(cols))
	binary.LittleEndian.PutUint32(dst[12:], uint32(rows))
	binary.LittleEndian.PutUint32(dst[16:], uint32(cursor.X))
	binary.LittleEndian.PutUint32(dst[20:], uint32(cursor.Y))
	binary.LittleEndian.PutUint32(dst[24:], uint32(cursor.Style))
	binary.LittleEndian.PutUint32(dst[28:], flags)
	binary.LittleEndian.PutUint32(dst[32:], uint32(t.Scrollback()))
	binary.LittleEndian.PutUint32(dst[36:], uint32(t.ViewOffset()))
	binary.LittleEndian.PutUint32(dst[40:], 0)
	binary.LittleEndian.PutUint32(dst[44:], 0)

	offset := screenHeaderSize
	for y := 0; y < rows; y++ {
		row := t.Row(y)
		binary.LittleEndian.PutUint32(dst[offset:], uint32(len(row.Cells)))
		offset += 4
		for _, cell := range row.Cells {
			binary.LittleEndian.PutUint32(dst[offset:], uint32(cell.R))
			binary.LittleEndian.PutUint32(dst[offset+4:], uint32(cell.FG))
			binary.LittleEndian.PutUint32(dst[offset+8:], uint32(cell.BG))
			binary.LittleEndian.PutUint16(dst[offset+12:], uint16(cell.Attr))
			width := uint8(1)
			var cellFlags uint8
			if cell.Attr&term.WideHead != 0 {
				width = 2
				cellFlags |= 1
			}
			if cell.Attr&term.WideTail != 0 {
				cellFlags |= 2
			}
			dst[offset+14] = width
			dst[offset+15] = cellFlags
			offset += screenCellSize
		}
	}
	return offset
}

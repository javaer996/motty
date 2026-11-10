package desktop

import (
	"bytes"
	"testing"
)

// mirror is a Presenter that keeps the picture it was given.
type mirror struct {
	width, height int
	pix           []byte
	updates       []Rect
}

func (m *mirror) Resize(width, height int) {
	m.width, m.height, m.pix = width, height, make([]byte, width*height*4)
}

func (m *mirror) Update(x, y, width, height int, pix []byte, stride int) {
	m.updates = append(m.updates, Rect{x, y, width, height})
	for row := 0; row < height; row++ {
		copy(m.pix[((y+row)*m.width+x)*4:], pix[row*stride:][:width*4])
	}
}

func TestPresentSendsOnlyWhatChangedAndEqualsTheFrame(t *testing.T) {
	frame := NewFrame(nil)
	frame.Resize(100, 80)
	var shown mirror
	if !frame.Present(&shown) || shown.width != 100 || shown.height != 80 {
		t.Fatal("the first Present must resize the presenter")
	}
	if frame.Present(&shown) {
		t.Fatal("Present reported a change when nothing changed")
	}
	shown.updates = nil
	frame.Fill(10, 10, 5, 5, 1, 2, 3)
	patch := bytes.Repeat([]byte{9, 8, 7, 0}, 6)
	frame.Update(50, 60, 3, 2, patch, 12)
	frame.Update(90, 70, 40, 40, bytes.Repeat([]byte{4, 4, 4, 0}, 1600), 160) // hangs over the corner
	frame.Copy(20, 20, 5, 5, 10, 10)
	if !frame.Present(&shown) {
		t.Fatal("no change reported")
	}
	if len(shown.updates) != 4 {
		t.Fatalf("updates = %v, want the four rectangles and nothing else", shown.updates)
	}
	if shown.updates[2] != (Rect{90, 70, 10, 10}) {
		t.Fatalf("a rectangle hanging over the edge was %v, want it clipped", shown.updates[2])
	}
	frame.mu.Lock()
	same := bytes.Equal(frame.pix, shown.pix)
	frame.mu.Unlock()
	if !same {
		t.Fatal("what the presenter holds differs from the frame")
	}
	if got := shown.pix[(22*100+22)*4:][:3]; !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("copied pixel = %v", got)
	}
}

func TestCopyHandlesOverlapInBothDirections(t *testing.T) {
	for _, shift := range []int{3, -3} {
		frame := NewFrame(nil)
		frame.Resize(20, 20)
		for row := 0; row < 20; row++ {
			frame.Fill(0, row, 20, 1, byte(row), 0, 0)
		}
		frame.Copy(0, 5+shift, 20, 10, 0, 5)
		for row := 5; row < 15; row++ {
			if got := frame.pix[((row+shift)*20)*4]; got != byte(row) {
				t.Fatalf("shift %d: row %d landed as %d", shift, row+shift, got)
			}
		}
	}
}

func TestManySmallUpdatesCollapseIntoOneBoundingRectangle(t *testing.T) {
	frame := NewFrame(nil)
	frame.Resize(200, 200)
	var shown mirror
	frame.Present(&shown)
	shown.updates = nil
	for index := range 100 {
		frame.Fill(index, index, 1, 1, 1, 1, 1)
	}
	frame.Present(&shown)
	if len(shown.updates) > maxDirty {
		t.Fatalf("%d rectangles for 100 updates", len(shown.updates))
	}
	covered := Rect{}
	for _, rect := range shown.updates {
		covered = covered.Union(rect)
	}
	if covered != (Rect{0, 0, 100, 100}) {
		t.Fatalf("covered = %v", covered)
	}
}

func TestPresentBoundsOverlappingUpdatesToOnePicture(t *testing.T) {
	frame := NewFrame(nil)
	frame.Resize(320, 200)
	var shown mirror
	frame.Present(&shown)
	shown.updates = nil
	for index := range maxDirty - 1 {
		frame.Fill(0, 0, 320, 200, byte(index), 2, 3)
	}
	frame.Present(&shown)
	pixels := 0
	for _, rect := range shown.updates {
		pixels += rect.Width * rect.Height
	}
	if pixels > 320*200 {
		t.Fatalf("copied %d pixels for a %d-pixel picture", pixels, 320*200)
	}
	if !bytes.Equal(frame.pix, shown.pix) {
		t.Fatal("coalescing overlapping updates lost pixels")
	}
}

func TestResizeKeepsWhatTheOldPictureHadInsideTheNewBounds(t *testing.T) {
	notified := 0
	frame := NewFrame(func() { notified++ })
	frame.Resize(10, 10)
	frame.Fill(0, 0, 10, 10, 5, 5, 5)
	frame.Resize(4, 4)
	var shown mirror
	frame.Present(&shown)
	// The overlap is kept: a server does not always repaint everything after
	// its layout changed, and the watcher must not be left with a black screen.
	kept := bytes.Repeat([]byte{5, 5, 5, 0}, 16)
	if notified != 3 || shown.width != 4 || shown.height != 4 || !bytes.Equal(shown.pix, kept) {
		t.Fatalf("notified %d times, shown %dx%d, content %v", notified, shown.width, shown.height, shown.pix)
	}
}

func TestResizeToTheSameSizeLeavesThePictureAlone(t *testing.T) {
	notified := 0
	frame := NewFrame(func() { notified++ })
	frame.Resize(4, 4)
	frame.Fill(0, 0, 4, 4, 7, 7, 7)
	var shown mirror
	frame.Present(&shown)
	notified, shown.updates = 0, nil
	frame.Resize(4, 4)
	if frame.Present(&shown) {
		t.Fatal("a resize to the same size asked for an update")
	}
	if notified != 0 || len(shown.updates) != 0 {
		t.Fatalf("notified %d times, %d updates", notified, len(shown.updates))
	}
}

func BenchmarkFrameUpdateAndPresent(b *testing.B) {
	frame := NewFrame(nil)
	frame.Resize(1920, 1080)
	var shown mirror
	frame.Present(&shown)
	patch := make([]byte, 64*64*4)
	b.ReportAllocs()
	for b.Loop() {
		frame.Update(100, 100, 64, 64, patch, 64*4)
		shown.updates = shown.updates[:0]
		frame.Present(&shown)
	}
}

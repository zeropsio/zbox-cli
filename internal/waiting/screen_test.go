package waiting

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

var ansi = regexp.MustCompile("\033\\[[0-9;]*[A-Za-z]")

// A frame line wider than the terminal wraps, and the redraw then moves the cursor up too few rows and starts eating the scrollback above it.
func TestFrameFitsTheTerminal(t *testing.T) {
	for _, width := range []int{40, 60, 80, 120, 200} {
		s := New(3 * time.Minute)
		s.color = true
		s.step = "Adding the default base image"
		for i, line := range s.frame(width, 40) {
			if visible := len([]rune(ansi.ReplaceAllString(line, ""))); visible > width {
				t.Errorf("width %d, line %d takes %d columns", width, i, visible)
			}
		}
	}
}

// A note has two lines on a standard terminal and is cut when it needs a third one, which would drop the part that usually carries the catch.
func TestNotesFitInTheirRows(t *testing.T) {
	for i, note := range notes {
		lines := wrap(note, contentWidth, noteRows)
		if strings.HasSuffix(lines[noteRows-1], "…") {
			t.Errorf("note %d does not fit in %d rows: %q", i, noteRows, note)
		}
	}
}

// Every frame has to have the same height, the redraw addresses the previous one by a line count. The notes are the part that varies, a short one has to take the same room as a long one.
func TestFrameHeightIsStable(t *testing.T) {
	s := New(3 * time.Minute)
	s.step = "Reading the URL and the API key of the workspace"
	first := len(s.frame(80, 40))
	for i := range notes {
		s.offset = i
		if got := len(s.frame(80, 40)); got != first {
			t.Errorf("note %d changed the frame height from %d to %d", i, first, got)
		}
	}
}

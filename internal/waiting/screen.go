package waiting

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

const (
	frameInterval = 100 * time.Millisecond
	noteInterval  = 9 * time.Second
	noteRows      = 2
	stepSuffix    = "..."
	contentWidth  = 78
	minWidth      = 44
	minHeight     = 24

	reset     = "\033[0m"
	dim       = "\033[2m"
	bold      = "\033[1m"
	accent    = "\033[38;5;42m"
	hideCurso = "\033[?25l"
	showCurso = "\033[?25h"
)

// Screen is the progress view of a wait that takes minutes and produces almost no output of its own. A terminal that prints one line and then goes quiet reads as a hang.
// It redraws in place on a TTY, when stdout is a pipe or a CI log the same events come out as plain lines instead, because control sequences there are noise.
type Screen struct {
	out      *os.File
	tty      bool
	color    bool
	baseline time.Duration
	started  time.Time
	notes    []string
	offset   int

	mu      sync.Mutex
	step    string
	floor   float64
	drawn   int
	stopped bool
	paused  bool
	done    chan struct{}
}

// New takes the expected duration of the whole wait, which is what the percentage is measured against. It is an estimate, not a deadline, nothing is cancelled when it passes.
func New(baseline time.Duration) *Screen {
	out := os.Stdout
	tty := term.IsTerminal(int(out.Fd()))
	notes := Notes()
	return &Screen{
		out:      out,
		tty:      tty,
		color:    tty && os.Getenv("NO_COLOR") == "",
		baseline: baseline,
		started:  time.Now(),
		notes:    notes,
		// Starting somewhere else each run means a second setup does not replay the first one note for note.
		offset: rand.Intn(len(notes)),
		done:   make(chan struct{}),
	}
}

func (s *Screen) Start() {
	if !s.tty {
		return
	}
	fmt.Fprint(s.out, hideCurso)
	go func() {
		ticker := time.NewTicker(frameInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-ticker.C:
				s.draw()
			}
		}
	}()
}

// Step names what is being waited for and the percentage that stage is worth. The floor pulls the bar forward when a stage lands early, the clock alone would lag behind it.
func (s *Screen) Step(text string, floor int) {
	s.mu.Lock()
	s.step = text
	if f := float64(floor); f > s.floor {
		s.floor = f
	}
	s.mu.Unlock()
	if !s.tty {
		fmt.Fprintln(s.out, text+stepSuffix)
		return
	}
	s.draw()
}

// Pause wipes the frame and gives the terminal to the caller for a while, for a step that has to print and read, such as the browser hand-over. Resume takes it back.
func (s *Screen) Pause() {
	if !s.tty {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.paused {
		return
	}
	s.paused = true
	if s.drawn > 0 {
		fmt.Fprintf(s.out, "\033[%dA\033[J", s.drawn)
		s.drawn = 0
	}
	fmt.Fprint(s.out, showCurso)
}

func (s *Screen) Resume() {
	if !s.tty {
		return
	}
	s.mu.Lock()
	if s.stopped || !s.paused {
		s.mu.Unlock()
		return
	}
	s.paused = false
	fmt.Fprint(s.out, hideCurso)
	s.mu.Unlock()
	s.draw()
}

// Stop wipes the frame and gives the cursor back. It is safe to call twice, the caller defers it and the happy path calls it before printing the result.
func (s *Screen) Stop() {
	if !s.tty {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	close(s.done)
	if s.drawn > 0 {
		fmt.Fprintf(s.out, "\033[%dA\033[J", s.drawn)
		s.drawn = 0
	}
	fmt.Fprint(s.out, showCurso)
}

func (s *Screen) draw() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.paused {
		return
	}
	width, height := s.size()
	lines := s.frame(width, height)

	var b strings.Builder
	if s.drawn > 0 {
		fmt.Fprintf(&b, "\033[%dA", s.drawn)
	}
	for _, line := range lines {
		// Erasing the line first, the previous frame can be wider than this one and a shorter note would leave the tail of the old one behind.
		b.WriteString("\033[2K")
		b.WriteString(line)
		b.WriteString("\n")
	}
	s.drawn = len(lines)
	fmt.Fprint(s.out, b.String())
}

// frame lays the whole view out inside a centred block of at most contentWidth columns. Nothing may exceed the terminal width, a line that wraps adds a row the redraw does not know about
// and the next frame then eats the line above it.
func (s *Screen) frame(width, height int) []string {
	elapsed := time.Since(s.started)
	content := min(max(width-4, 20), contentWidth)
	pad := strings.Repeat(" ", max((width-content)/2, 2))

	lines := []string{""}
	// A short or narrow terminal drops the wordmark rather than scrolling the frame away, the redraw only works while the whole frame fits on screen.
	if width >= minWidth && height >= minHeight {
		inner := pad + strings.Repeat(" ", max((content-bannerWidth())/2, 0))
		for _, line := range banner(elapsed.Seconds(), s.color) {
			lines = append(lines, inner+line)
		}
		lines = append(lines, "")
	}
	lines = append(lines, pad+s.bar(content), "")
	// The trailing dots are added here rather than in every step string, a step is a name and the fact that it is still running is the screen's business.
	lines = append(lines, pad+s.paint(bold, truncate(s.step+stepSuffix, content)), "")
	for _, line := range wrap(s.note(elapsed), content, noteRows) {
		lines = append(lines, pad+s.paint(dim, line))
	}
	lines = append(lines, "")
	return lines
}

func truncate(text string, width int) string {
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width < 2 {
		return ""
	}
	return string(runes[:width-1]) + "…"
}

// wrap breaks a note on word boundaries and always returns exactly rows lines, blank ones included. The frame is redrawn by line count, so a two line note and a one line note
// have to take up the same room.
func wrap(text string, width, rows int) []string {
	var out []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case len(line)+1+len(word) <= width:
			line += " " + word
		default:
			out = append(out, line)
			line = word
		}
	}
	if line != "" {
		out = append(out, line)
	}
	// A note that does not fit loses its tail. Growing the frame instead would shift everything the redraw counted on.
	if len(out) > rows {
		out = out[:rows]
		out[rows-1] = truncate(out[rows-1]+" …", width)
	}
	for len(out) < rows {
		out = append(out, "")
	}
	return out
}

// percent has no completion signal to report, so it follows a curve that reaches 95% at the baseline and keeps creeping after it, never parking on 100% while work is still going.
func (s *Screen) percent() float64 {
	p := 99 * (1 - math.Exp(-3*float64(time.Since(s.started))/float64(s.baseline)))
	if p < s.floor {
		return s.floor
	}
	return p
}

func (s *Screen) bar(width int) string {
	pct := s.percent()
	cells := min(max(width-22, 10), 40)
	filled := int(math.Round(pct / 100 * float64(cells)))
	return fmt.Sprintf("%s%s  %3.0f%%  %s",
		s.paint(accent, strings.Repeat("█", filled)),
		s.paint(dim, strings.Repeat("░", cells-filled)),
		pct,
		s.paint(dim, elapsedString(time.Since(s.started))),
	)
}

func (s *Screen) note(elapsed time.Duration) string {
	return s.notes[(s.offset+int(elapsed/noteInterval))%len(s.notes)]
}

func (s *Screen) paint(seq, text string) string {
	if !s.color || text == "" {
		return text
	}
	return seq + text + reset
}

func (s *Screen) size() (int, int) {
	width, height, err := term.GetSize(int(s.out.Fd()))
	if err != nil || width <= 0 {
		return 80, 24
	}
	return width, height
}

func elapsedString(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
}

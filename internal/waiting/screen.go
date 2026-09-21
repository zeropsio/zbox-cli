package waiting

import (
	"fmt"
	"math/rand"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

const (
	noteInterval = 9 * time.Second
	stepSuffix   = "..."

	reset = "\033[0m"
	dim   = "\033[2m"
)

// Screen is the plain text view of a wait that takes minutes and produces almost no output of its own. A terminal that prints one line and then goes quiet reads as a hang,
// so every stage prints a line and, on a TTY, a note about workspaces follows every few seconds. Nothing is redrawn, the lines scroll like any other output.
type Screen struct {
	out   *os.File
	tty   bool
	color bool
	notes []string
	next  int

	mu      sync.Mutex
	stopped bool
	paused  bool
	done    chan struct{}
}

func New() *Screen {
	out := os.Stdout
	tty := term.IsTerminal(int(out.Fd()))
	notes := Notes()
	return &Screen{
		out:   out,
		tty:   tty,
		color: tty && os.Getenv("NO_COLOR") == "",
		notes: notes,
		// Starting somewhere else each run means a second setup does not replay the first one note for note.
		next: rand.Intn(len(notes)),
		done: make(chan struct{}),
	}
}

// Start begins the notes. A pipe or a CI log gets the steps only, a tip about parking sandboxes is noise there.
func (s *Screen) Start() {
	if !s.tty {
		return
	}
	go func() {
		ticker := time.NewTicker(noteInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-ticker.C:
				s.printNote()
			}
		}
	}()
}

// Step names what is being waited for. The trailing dots are added here rather than in every step string, a step is a name and the fact that it is still running is the screen's business.
func (s *Screen) Step(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintln(s.out, text+stepSuffix)
}

// Pause holds the notes while the caller prints and reads on the terminal, such as the browser hand-over, a note arriving in the middle of a prompt would be confusing. Resume lets them go on.
func (s *Screen) Pause() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = true
}

func (s *Screen) Resume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = false
}

// Stop ends the notes. It is safe to call twice, the caller defers it and the happy path calls it before printing the result.
func (s *Screen) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	close(s.done)
}

func (s *Screen) printNote() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.paused {
		return
	}
	note := s.notes[s.next%len(s.notes)]
	s.next++
	if s.color {
		note = dim + note + reset
	}
	fmt.Fprintln(s.out, note)
}

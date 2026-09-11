package waiting

import (
	"math"
	"strings"
)

// The wordmark is assembled from equal width glyphs so that a column index means the same thing in every row, which is what lets the wave lift one column at a time.
var (
	glyphZ = []string{"       ", "       ", "██████ ", "  ███  ", "██████ "}
	glyphB = []string{"██     ", "██     ", "██████ ", "██   ██", "██████ "}
	glyphO = []string{"       ", "       ", " █████ ", "██   ██", " █████ "}
	glyphX = []string{"       ", "       ", "██   ██", "  ███  ", "██   ██"}

	wordmark = buildWordmark()
)

const (
	// The glyphs are five rows and a letter can be lifted one row either way, so the canvas is seven.
	bannerRows    = 7
	glyphWidth    = 7
	letterStride  = glyphWidth + 1
	waveSpeed     = 0.45
	letterPhase   = math.Pi / 2
	shimmerSpeed  = 0.35
	shimmerLength = 19.0
)

// palette is a ramp from a dark green to a pale one, the wave picks a shade from it so the crest reads as a highlight travelling along the word.
var palette = []string{"\033[38;5;22m", "\033[38;5;28m", "\033[38;5;35m", "\033[38;5;42m", "\033[38;5;48m", "\033[38;5;85m", "\033[38;5;121m"}

func buildWordmark() []string {
	out := make([]string, len(glyphZ))
	for i := range out {
		out[i] = strings.Join([]string{glyphZ[i], glyphB[i], glyphO[i], glyphX[i]}, " ")
	}
	return out
}

func bannerWidth() int {
	return len([]rune(wordmark[0]))
}

// banner draws the wordmark rippling. Whole letters bob on a sine a quarter period apart, lifting single columns instead would shear the strokes and the word stops being readable.
// The shade rides a second, slower wave that does run per column, a highlight travelling through a letter is exactly what a solid block of colour needs.
func banner(seconds float64, color bool) []string {
	glyph := make([][]rune, len(wordmark))
	for i, row := range wordmark {
		glyph[i] = []rune(row)
	}
	width := bannerWidth()
	shift := make([]int, width)
	shade := make([]int, width)
	for col := range width {
		lift := math.Sin(2*math.Pi*seconds*waveSpeed - float64(col/letterStride)*letterPhase)
		shift[col] = int(math.Round(lift))
		shimmer := math.Sin(2 * math.Pi * (seconds*shimmerSpeed - float64(col)/shimmerLength))
		shade[col] = int(math.Round((shimmer + 1) / 2 * float64(len(palette)-1)))
	}

	out := make([]string, bannerRows)
	for row := range bannerRows {
		var b strings.Builder
		painted := -1
		for col := range width {
			ch := ' '
			// The canvas has one spare row above the glyphs, so a column lifted by one still lands inside it.
			if src := row - 1 - shift[col]; src >= 0 && src < len(glyph) {
				ch = glyph[src][col]
			}
			if color && ch != ' ' && shade[col] != painted {
				b.WriteString(palette[shade[col]])
				painted = shade[col]
			}
			b.WriteRune(ch)
		}
		if color && painted >= 0 {
			b.WriteString(reset)
		}
		out[row] = b.String()
	}
	return out
}

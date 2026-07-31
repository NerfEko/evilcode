package tui

import (
	"math"
	"strings"
	"testing"
	"time"

	"evilcode/internal/theme"
)

func TestPulseIsTriangular(t *testing.T) {
	// In fast, out fast, brightest at the midpoint.
	if got := pulsePhase(0); got != 0 {
		t.Errorf("start = %.3f, want 0", got)
	}
	if got := pulsePhase(0.5); math.Abs(got-1) > 1e-9 {
		t.Errorf("midpoint = %.3f, want 1", got)
	}
	if got := pulsePhase(1); math.Abs(got) > 1e-9 {
		t.Errorf("end = %.3f, want 0", got)
	}
	// Symmetric around the midpoint.
	for _, d := range []float64{0.1, 0.2, 0.4} {
		a, b := pulsePhase(0.5-d), pulsePhase(0.5+d)
		if math.Abs(a-b) > 1e-9 {
			t.Errorf("asymmetric at ±%.1f: %.3f vs %.3f", d, a, b)
		}
	}
}

func TestSpotlightRisesFastAndDecaysSlow(t *testing.T) {
	// The asymmetry is the point: arriving should feel immediate and leaving
	// should feel unhurried.
	peak, peakAt := 0.0, 0.0
	for i := 0; i <= 100; i++ {
		x := float64(i) / 100
		if v := spotlightPhase(x); v > peak {
			peak, peakAt = v, x
		}
	}
	if peakAt > 0.45 {
		t.Errorf("spotlight peaks at %.2f; it should rise fast", peakAt)
	}
	if peak < 0.3 {
		t.Errorf("spotlight peak is only %.2f", peak)
	}
	if got := spotlightPhase(1); got > 1e-9 {
		t.Errorf("spotlight should end at 0, got %.3f", got)
	}
	if got := spotlightPhase(0); got > 1e-9 {
		t.Errorf("spotlight should start at 0, got %.3f", got)
	}
}

func TestShimmerSweepsLeftToRight(t *testing.T) {
	// The lit position should advance monotonically across the row.
	var lastPeak float64 = -1
	for _, t0 := range []float64{0.1, 0.3, 0.5, 0.7} {
		peak, peakAt := 0.0, 0.0
		for i := 0; i <= 100; i++ {
			pos := float64(i) / 100
			if v := shimmerIntensity(t0, pos); v > peak {
				peak, peakAt = v, pos
			}
		}
		if peak == 0 {
			continue
		}
		if peakAt < lastPeak {
			t.Errorf("sweep moved backwards at t=%.1f: %.2f after %.2f", t0, peakAt, lastPeak)
		}
		lastPeak = peakAt
	}
	if lastPeak <= 0 {
		t.Error("the sweep never lit anything")
	}
}

func TestShimmerBandIsNarrow(t *testing.T) {
	lit := 0
	for i := 0; i <= 100; i++ {
		if shimmerIntensity(0.5, float64(i)/100) > 0 {
			lit++
		}
	}
	// Roughly twice the half-width, with slack for the discretization.
	if lit > int(ShimmerWidth*2*100)+4 {
		t.Errorf("%d%% of the row is lit at once; the band should be narrow", lit)
	}
}

func TestShimmerFadesOut(t *testing.T) {
	// The sweep should not stop dead at the end of the animation.
	early := shimmerIntensity(0.2, clampUnit(0.2*1.15))
	late := shimmerIntensity(0.9, clampUnit(0.9*1.15))
	if late >= early {
		t.Errorf("shimmer did not fade: %.3f at t=0.2, %.3f at t=0.9", early, late)
	}
}

func TestEntryAnimationProgress(t *testing.T) {
	a := NewEntryAnimation(3)
	if _, running := a.Progress(a.Started); !running {
		t.Error("a fresh animation should be running")
	}
	if _, running := a.Progress(a.Started.Add(EntryDuration)); running {
		t.Error("the animation should end after its duration")
	}
	got, _ := a.Progress(a.Started.Add(EntryDuration / 2))
	if math.Abs(got-0.5) > 0.05 {
		t.Errorf("halfway progress = %.3f, want about 0.5", got)
	}
}

func TestNoAnimationWithoutABlock(t *testing.T) {
	var a EntryAnimation
	a.Block = -1
	if _, running := a.Progress(time.Now()); running {
		t.Error("an unset animation must not run")
	}
}

func TestApplyEntryAnimationKeepsText(t *testing.T) {
	// The row has to stay readable while it animates; the effect must not eat
	// or reorder characters.
	fg, bg := theme.RGB(240, 240, 240), theme.RGB(42, 36, 64)
	const text = "7› what does this function do?"

	for _, t0 := range []float64{0, 0.25, 0.5, 0.75, 1} {
		got := ApplyEntryAnimation(text, t0, fg, bg)
		if plain := plainText(got); plain != text {
			t.Errorf("t=%.2f changed the text: %q", t0, plain)
		}
	}
}

func TestApplyEntryAnimationActuallyChangesColor(t *testing.T) {
	fg, bg := theme.RGB(240, 240, 240), theme.RGB(42, 36, 64)
	const text = "hello world"

	start := ApplyEntryAnimation(text, 0.01, fg, bg)
	mid := ApplyEntryAnimation(text, 0.5, fg, bg)
	if start == mid {
		t.Error("the animation produced identical frames")
	}
	// At the midpoint the pulse is at its peak, so the row should be warmer.
	if !strings.Contains(mid, "38;2;") {
		t.Errorf("no truecolor foreground emitted:\n%q", mid)
	}
}

func TestApplyEntryAnimationEmptyRow(t *testing.T) {
	fg, bg := theme.RGB(240, 240, 240), theme.RGB(42, 36, 64)
	if got := ApplyEntryAnimation("", 0.5, fg, bg); got != "" {
		t.Errorf("empty row = %q", got)
	}
}

func TestArtRendersAShape(t *testing.T) {
	// The sampler-to-glyph path should produce something with structure, not a
	// uniform block or a blank.
	for _, v := range []Variant{VariantEye, VariantBlackhole} {
		rows := RenderArt(SamplerFor(v), 60, 14, 0, true)
		if len(rows) != 14 {
			t.Fatalf("%s: rows = %d, want 14", v, len(rows))
		}
		joined := plainText(strings.Join(rows, "\n"))
		nonBlank := 0
		for _, r := range joined {
			if r != ' ' && r != '\n' {
				nonBlank++
			}
		}
		if nonBlank < 40 {
			t.Errorf("%s: only %d non-blank cells; the art is empty", v, nonBlank)
		}
		if nonBlank > 60*14*9/10 {
			t.Errorf("%s: %d non-blank cells; the art is a solid block", v, nonBlank)
		}
	}
}

func TestArtIsDeterministicAtAGivenTime(t *testing.T) {
	// Frozen mode depends on the same elapsed time producing the same frame.
	first := RenderArt(SamplerFor(VariantEye), 40, 10, 0, true)
	for i := 0; i < 3; i++ {
		again := RenderArt(SamplerFor(VariantEye), 40, 10, 0, true)
		for j := range first {
			if first[j] != again[j] {
				t.Fatalf("row %d differs between renders at the same time", j)
			}
		}
	}
}

func TestArtAnimatesOverTime(t *testing.T) {
	a := RenderArt(SamplerFor(VariantBlackhole), 40, 10, 0, true)
	b := RenderArt(SamplerFor(VariantBlackhole), 40, 10, 1.5, true)
	same := true
	for i := range a {
		if a[i] != b[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("the art did not change over time")
	}
}

func TestPickVariantIsStable(t *testing.T) {
	first := PickVariant("dracula")
	for i := 0; i < 5; i++ {
		if got := PickVariant("dracula"); got != first {
			t.Fatalf("variant changed between calls: %s then %s", first, got)
		}
	}
}

func TestArtHandlesDegenerateSizes(t *testing.T) {
	// A zero-width or zero-height art block happens on a tiny terminal and must
	// not panic.
	for _, sz := range [][2]int{{0, 10}, {10, 0}, {0, 0}, {1, 1}} {
		RenderArt(SamplerFor(VariantEye), sz[0], sz[1], 0, true)
	}
}

func TestFrozenArtIgnoresElapsed(t *testing.T) {
	// Under EVILCODE_DETERMINISTIC animations freeze at frame 0, which is what
	// makes golden frames reproducible (invariant 5).
	a := RenderArt(SamplerFor(VariantEye), 40, 10, 0, false)
	b := RenderArt(SamplerFor(VariantEye), 40, 10, 9.5, false)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("row %d changed with animation off", i)
		}
	}
}

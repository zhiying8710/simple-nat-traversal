package fyneapp

import (
	"image/color"
	"testing"

	fyne "fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func TestSNTThemeForcesReadableLightPalette(t *testing.T) {
	t.Parallel()

	th := newSNTTheme()

	if got := asNRGBA(th.Color(theme.ColorNameBackground, theme.VariantDark)); got != (color.NRGBA{R: 0xF5, G: 0xEE, B: 0xE5, A: 0xFF}) {
		t.Fatalf("background = %#v", got)
	}
	if got := asNRGBA(th.Color(theme.ColorNameForeground, theme.VariantDark)); got != (color.NRGBA{R: 0x26, G: 0x2C, B: 0x34, A: 0xFF}) {
		t.Fatalf("foreground = %#v", got)
	}
	if got := asNRGBA(th.Color(theme.ColorNameDisabled, theme.VariantDark)); got != (color.NRGBA{R: 0x73, G: 0x68, B: 0x5D, A: 0xFF}) {
		t.Fatalf("disabled = %#v", got)
	}
}

func TestSNTPanelThemeForcesReadableDarkPalette(t *testing.T) {
	t.Parallel()

	th := newSNTPanelTheme()

	if got := asNRGBA(th.Color(theme.ColorNameBackground, theme.VariantLight)); got != darkPanelFill {
		t.Fatalf("background = %#v", got)
	}
	if got := asNRGBA(th.Color(theme.ColorNameForeground, theme.VariantLight)); got != (color.NRGBA{R: 0xF7, G: 0xF0, B: 0xE8, A: 0xFF}) {
		t.Fatalf("foreground = %#v", got)
	}
	if got := asNRGBA(th.Color(theme.ColorNameDisabled, theme.VariantLight)); got != (color.NRGBA{R: 0xA2, G: 0xB1, B: 0xC2, A: 0xFF}) {
		t.Fatalf("disabled = %#v", got)
	}
}

func TestTitleAndMutedBreakLabelsUseStableWrapping(t *testing.T) {
	t.Parallel()

	title := newTitleLabel("Simple NAT Traversal")
	if title.Wrapping != fyne.TextWrapOff {
		t.Fatalf("title wrapping = %v", title.Wrapping)
	}
	if title.Truncation != fyne.TextTruncateEllipsis {
		t.Fatalf("title truncation = %v", title.Truncation)
	}
	if !title.TextStyle.Bold {
		t.Fatal("title label should be bold")
	}

	muted := newMutedBreakLabel("config/path")
	if muted.Wrapping != fyne.TextWrapBreak {
		t.Fatalf("muted wrapping = %v", muted.Wrapping)
	}
	if muted.Importance != widget.LowImportance {
		t.Fatalf("muted importance = %v", muted.Importance)
	}
}

func asNRGBA(value color.Color) color.NRGBA {
	return color.NRGBAModel.Convert(value).(color.NRGBA)
}

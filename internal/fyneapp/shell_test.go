package fyneapp

import (
	"testing"

	fyne "fyne.io/fyne/v2"
	fyneTest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestActionRowExpandsInputField(t *testing.T) {
	t.Parallel()

	fyneApp := fyneTest.NewApp()
	t.Cleanup(func() {
		fyneApp.Quit()
	})

	entry := widget.NewEntry()
	button := widget.NewButton("Kick by Name", nil)
	row := newActionRow(entry, button)

	row.Resize(fyne.NewSize(520, 48))

	if entry.Size().Width <= button.Size().Width {
		t.Fatalf("expected entry width > button width, got entry=%v button=%v", entry.Size(), button.Size())
	}
	if entry.Size().Width < 240 {
		t.Fatalf("expected entry to take most of the row width, got %v", entry.Size())
	}
}

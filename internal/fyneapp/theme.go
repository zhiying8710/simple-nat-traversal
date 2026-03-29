package fyneapp

import (
	"image/color"

	fyne "fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

var (
	sntAccentOrange = color.NRGBA{R: 0xD9, G: 0x74, B: 0x2A, A: 0xFF}
	sntAccentWarm   = color.NRGBA{R: 0xF4, G: 0xE4, B: 0xD3, A: 0xFF}
)

type paletteTheme struct {
	base    fyne.Theme
	variant fyne.ThemeVariant
	colors  map[fyne.ThemeColorName]color.Color
}

func newSNTTheme() fyne.Theme {
	return &paletteTheme{
		base:    theme.DefaultTheme(),
		variant: theme.VariantLight,
		colors: map[fyne.ThemeColorName]color.Color{
			theme.ColorNameBackground:          color.NRGBA{R: 0xF5, G: 0xEE, B: 0xE5, A: 0xFF},
			theme.ColorNameButton:              color.NRGBA{R: 0xEB, G: 0xE0, B: 0xD2, A: 0xFF},
			theme.ColorNameDisabledButton:      color.NRGBA{R: 0xE4, G: 0xD7, B: 0xC7, A: 0xFF},
			theme.ColorNameForeground:          color.NRGBA{R: 0x26, G: 0x2C, B: 0x34, A: 0xFF},
			theme.ColorNameDisabled:            color.NRGBA{R: 0x73, G: 0x68, B: 0x5D, A: 0xFF},
			theme.ColorNameHeaderBackground:    color.NRGBA{R: 0xEF, G: 0xE4, B: 0xD7, A: 0xFF},
			theme.ColorNameInputBackground:     color.NRGBA{R: 0xFC, G: 0xF8, B: 0xF3, A: 0xFF},
			theme.ColorNameInputBorder:         color.NRGBA{R: 0xCE, G: 0xBE, B: 0xAA, A: 0xFF},
			theme.ColorNameMenuBackground:      color.NRGBA{R: 0xFB, G: 0xF7, B: 0xF1, A: 0xFF},
			theme.ColorNameOverlayBackground:   color.NRGBA{R: 0x10, G: 0x1A, B: 0x26, A: 0xA8},
			theme.ColorNamePlaceHolder:         color.NRGBA{R: 0x92, G: 0x84, B: 0x77, A: 0xFF},
			theme.ColorNamePrimary:             sntAccentOrange,
			theme.ColorNameForegroundOnPrimary: color.NRGBA{R: 0xFF, G: 0xF6, B: 0xEC, A: 0xFF},
			theme.ColorNameSelection:           color.NRGBA{R: 0xE5, G: 0xAA, B: 0x78, A: 0x66},
			theme.ColorNameSeparator:           color.NRGBA{R: 0xD0, G: 0xBE, B: 0xAB, A: 0xFF},
			theme.ColorNameHover:               color.NRGBA{R: 0xF0, G: 0xC8, B: 0x9F, A: 0x52},
			theme.ColorNamePressed:             color.NRGBA{R: 0xCF, G: 0x9B, B: 0x70, A: 0x66},
			theme.ColorNameFocus:               color.NRGBA{R: 0xD9, G: 0x74, B: 0x2A, A: 0x88},
		},
	}
}

func newSNTPanelTheme() fyne.Theme {
	return &paletteTheme{
		base:    theme.DarkTheme(),
		variant: theme.VariantDark,
		colors: map[fyne.ThemeColorName]color.Color{
			theme.ColorNameBackground:          color.NRGBA{R: 0x10, G: 0x22, B: 0x37, A: 0xFF},
			theme.ColorNameButton:              color.NRGBA{R: 0x17, G: 0x2F, B: 0x47, A: 0xFF},
			theme.ColorNameDisabledButton:      color.NRGBA{R: 0x1C, G: 0x34, B: 0x4C, A: 0xFF},
			theme.ColorNameForeground:          color.NRGBA{R: 0xF7, G: 0xF0, B: 0xE8, A: 0xFF},
			theme.ColorNameDisabled:            color.NRGBA{R: 0xA2, G: 0xB1, B: 0xC2, A: 0xFF},
			theme.ColorNameHeaderBackground:    color.NRGBA{R: 0x13, G: 0x28, B: 0x3F, A: 0xFF},
			theme.ColorNameInputBackground:     color.NRGBA{R: 0x0D, G: 0x1D, B: 0x2E, A: 0xFF},
			theme.ColorNameInputBorder:         color.NRGBA{R: 0x42, G: 0x59, B: 0x73, A: 0xFF},
			theme.ColorNameMenuBackground:      color.NRGBA{R: 0x10, G: 0x22, B: 0x37, A: 0xFF},
			theme.ColorNameOverlayBackground:   color.NRGBA{R: 0x06, G: 0x10, B: 0x1C, A: 0xCC},
			theme.ColorNamePlaceHolder:         color.NRGBA{R: 0x7E, G: 0x93, B: 0xAA, A: 0xFF},
			theme.ColorNamePrimary:             sntAccentOrange,
			theme.ColorNameForegroundOnPrimary: color.NRGBA{R: 0xFF, G: 0xF6, B: 0xEC, A: 0xFF},
			theme.ColorNameSelection:           color.NRGBA{R: 0xD9, G: 0x74, B: 0x2A, A: 0x66},
			theme.ColorNameSeparator:           color.NRGBA{R: 0x38, G: 0x51, B: 0x6D, A: 0xFF},
			theme.ColorNameHover:               color.NRGBA{R: 0x44, G: 0x60, B: 0x7C, A: 0x88},
			theme.ColorNamePressed:             color.NRGBA{R: 0x58, G: 0x74, B: 0x92, A: 0x99},
			theme.ColorNameFocus:               color.NRGBA{R: 0xD9, G: 0x74, B: 0x2A, A: 0x99},
		},
	}
}

func (t *paletteTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if value, ok := t.colors[name]; ok {
		return value
	}
	return t.base.Color(name, t.variant)
}

func (t *paletteTheme) Font(style fyne.TextStyle) fyne.Resource {
	return t.base.Font(style)
}

func (t *paletteTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return t.base.Icon(name)
}

func (t *paletteTheme) Size(name fyne.ThemeSizeName) float32 {
	return t.base.Size(name)
}

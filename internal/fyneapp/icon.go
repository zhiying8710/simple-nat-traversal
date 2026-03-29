package fyneapp

import (
	_ "embed"

	fyne "fyne.io/fyne/v2"
)

//go:embed assets/app_icon.svg
var appIconSVG []byte

var appIcon = fyne.NewStaticResource("app_icon.svg", appIconSVG)

func appIconResource() fyne.Resource {
	return appIcon
}

// AIT — a terminal that runs one AI agent across several accounts and
// passes the conversation to the next account when one runs out of usage.
package main

import (
	"embed"
	"fmt"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend
var assets embed.FS

func main() {
	store, err := NewStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ait:", err)
		os.Exit(1)
	}
	app := NewApp(store)

	err = wails.Run(&options.App{
		Title:            "AIT",
		Width:            1100,
		Height:           680,
		MinWidth:         420,
		MinHeight:        260,
		Frameless:        true,
		BackgroundColour: &options.RGBA{R: 12, G: 12, B: 12, A: 255},
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		OnBeforeClose:    app.beforeClose,
		DragAndDrop:      &options.DragAndDrop{EnableFileDrop: true},
		Bind:             []any{app},
		Windows: &windows.Options{
			Theme:                windows.Dark,
			DisableWindowIcon:    false,
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ait:", err)
		os.Exit(1)
	}
}

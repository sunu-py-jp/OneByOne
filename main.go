package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()
	err := wails.Run(&options.App{
		Title: "OneByOne", Width: 1440, Height: 940, MinWidth: 1050, MinHeight: 700, Frameless: false,
		BackgroundColour: &options.RGBA{R: 246, G: 247, B: 250, A: 255},
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        app.startup, OnShutdown: app.shutdown, OnBeforeClose: app.beforeClose, Bind: []interface{}{app},
		Mac: &mac.Options{TitleBar: mac.TitleBarDefault()},
	})
	if err != nil {
		log.Fatal(err)
	}
}

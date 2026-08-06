package main

import (
	"context"
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/K3N4Y/atenea/internal/host"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// The shared outer assembly: the .env of the working directory, the workspace
	// root, the SQLite store and the provider service the terminal app also reads,
	// and the sitting. The app over it is only the Wails adapter. See internal/host.
	app := NewApp(host.New(context.Background(), host.Config{
		Dotenv: ".env",
	}))

	// Create application with options
	err := wails.Run(&options.App{
		Title:  "atenea",
		Width:  1024,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}

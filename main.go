package main

import (
	"context"
	"embed"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	appversion "github.com/alplix/lupinus/internal/version"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/icon.png
var iconPNG []byte

// systray.SetIcon expects an ICO payload on Windows (PNG bytes are silently
// rejected there). macOS/Linux tray backends are fine with the PNG, so only
// the tray icon itself is swapped by OS; the window icon and macOS About
// panel keep using iconPNG.
//
//go:embed build/windows/icon.ico
var iconICO []byte

// main calls wails.Run directly as the program's primary blocking call —
// deliberately NOT wrapped inside systray.Run (cardinalby/go-systray's own
// blocking event loop). `wails build` generates bindings by compiling and
// actually running this binary; if the program never reaches a point where
// it can exit on its own, that step hangs forever. The system tray is
// instead started from App.startup (see app.go's startTray), once Wails
// itself is already driving the process.
func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     appversion.Name,
		Width:     1280,
		Height:    800,
		MinWidth:  960,
		MinHeight: 640,
		BackgroundColour: &options.RGBA{
			R: 9, G: 7, B: 17, A: 255, // Space Black
		},
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "dev.alplix.lupinus",
			OnSecondInstanceLaunch: func(_ options.SecondInstanceData) {
				app.showWindow()
			},
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		OnBeforeClose: func(ctx context.Context) bool {
			app.hideWindow()
			return true
		},
		Windows: &windows.Options{
			WebviewIsTransparent: true,
			WindowIsTranslucent:  true,
			BackdropType:         windows.Acrylic,
			Theme:                windows.SystemDefault,
		},
		Mac: &mac.Options{
			About: &mac.AboutInfo{
				Title:   appversion.Name + " " + appversion.Version,
				Message: "Native cross-platform VNC client.\n\nCoded by " + appversion.Author + "\n" + appversion.Repo,
				Icon:    iconPNG,
			},
		},
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		println("Error:", err.Error())
		os.Exit(1)
	}
}

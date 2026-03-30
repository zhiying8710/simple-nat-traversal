package main

import (
	"flag"
	"io"
	"log"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"simple-nat-traversal/internal/buildinfo"
	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/control"
	"simple-nat-traversal/internal/desktopapp"
	"simple-nat-traversal/internal/logx"
	"simple-nat-traversal/internal/wailsapp"
)

func main() {
	configPath := flag.String("config", config.DefaultGUIClientConfigPath(), "path to client config")
	versionMode := flag.Bool("version", false, "print version information and exit")
	flag.Parse()

	if *versionMode {
		if _, err := os.Stdout.WriteString(buildinfo.String("snt-gui") + "\n"); err != nil {
			log.Fatalf("print version: %v", err)
		}
		return
	}

	executablePath, err := os.Executable()
	if err != nil {
		log.Fatalf("resolve executable path: %v", err)
	}

	logBuffer := control.NewLogBuffer(500)
	log.SetOutput(io.MultiWriter(os.Stderr, logBuffer))
	if cfg, err := config.LoadClientConfig(*configPath); err == nil {
		_, _ = logx.SetLevel(cfg.LogLevel)
	}

	backend := wailsapp.New(desktopapp.Dependencies{
		ExecutablePath: executablePath,
		ConfigPath:     *configPath,
		RuntimeManager: control.NewRuntimeManager(),
		Logs:           logBuffer,
	})

	if err := wails.Run(&options.App{
		Title:  "简单打洞组网",
		Width:  1440,
		Height: 940,
		AssetServer: &assetserver.Options{
			Assets: wailsapp.Assets,
		},
		BackgroundColour: &options.RGBA{R: 0xF5, G: 0xEE, B: 0xE5, A: 0xFF},
		OnStartup:        backend.Startup,
		OnShutdown:       backend.Shutdown,
		Bind: []interface{}{
			backend,
		},
	}); err != nil {
		log.Fatalf("run wails app: %v", err)
	}
}

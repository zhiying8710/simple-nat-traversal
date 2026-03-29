package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"simple-nat-traversal/internal/buildinfo"
	"simple-nat-traversal/internal/control"
	"simple-nat-traversal/internal/fyneapp"
)

func main() {
	configPath := flag.String("config", "client.json", "path to client config")
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

	app, err := fyneapp.New(fyneapp.Config{
		ExecutablePath: executablePath,
		ConfigPath:     *configPath,
		RuntimeManager: control.NewRuntimeManager(),
		Logs:           logBuffer,
	})
	if err != nil {
		log.Fatalf("create gui app: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx); err != nil {
		log.Fatalf("gui exited: %v", err)
	}
}

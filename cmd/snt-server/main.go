package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"simple-nat-traversal/internal/buildinfo"
	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/server"
)

func main() {
	configPath := flag.String("config", "server.json", "path to server config")
	versionMode := flag.Bool("version", false, "print version information and exit")
	flag.Parse()

	if *versionMode {
		if _, err := os.Stdout.WriteString(buildinfo.String("snt-server") + "\n"); err != nil {
			log.Fatalf("print version: %v", err)
		}
		return
	}

	cfg, err := config.LoadServerConfig(*configPath)
	if err != nil {
		log.Fatalf("load server config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("server exited: %v", err)
	}
}

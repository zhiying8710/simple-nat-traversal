package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"simple-nat-traversal/internal/buildinfo"
	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/logx"
	"simple-nat-traversal/internal/server"
)

func main() {
	configPath := flag.String("config", "server.json", "path to server config")
	versionMode := flag.Bool("version", false, "print version information and exit")
	setLogLevel := flag.String("set-log-level", "", "update server log_level, apply it to the running server, and exit")
	flag.Parse()

	if *versionMode && *setLogLevel != "" {
		log.Fatal("choose only one of -version or -set-log-level")
	}
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
	if _, err := logx.SetLevel(cfg.LogLevel); err != nil {
		log.Fatalf("set log level: %v", err)
	}
	if *setLogLevel != "" {
		level, err := config.NormalizeLogLevel(*setLogLevel)
		if err != nil {
			log.Fatalf("parse -set-log-level: %v", err)
		}
		cfg.LogLevel = level
		if err := config.SaveServerConfig(*configPath, cfg); err != nil {
			log.Fatalf("update server config: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resp, err := server.SetRuntimeLogLevel(ctx, cfg, level)
		if err != nil {
			log.Fatalf("server log level saved to config but failed to update running server: %v", err)
		}
		if _, err := os.Stdout.WriteString(resp.LogLevel + "\n"); err != nil {
			log.Fatalf("print log level: %v", err)
		}
		return
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

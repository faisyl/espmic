// Package main is the entrypoint for the ESPMIC audio server (spec §3).
//
// It loads config, opens persistence, builds the Server (which ties together
// registries, control sessions, RTP receiver, decoder, PCM bus, recorder and
// live output), registers the HTTP API + metrics, and runs until SIGINT/SIGTERM
// triggers a graceful shutdown.
package main

import (
	"context"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"espmic/server/internal/api"
	"espmic/server/internal/audio"
	"espmic/server/internal/config"
	"espmic/server/internal/server"
	"espmic/server/web"
)

// Build-time stamped values (set via -X in .goreleaser.yaml; dev defaults).
var version = "dev"
var commit = "none"
var date = "unknown"

func main() {
	log.Printf("espmic-server version=%s commit=%s date=%s", version, commit, date)
	cfg := config.Load()

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("server: %v", err)
	}
	defer srv.Close()

	mux := http.NewServeMux()
	api.RegisterRoutes(mux, cfg, srv)
	api.SetVersion(version)
	// WebSocket live PCM output at /api/live (spec §14)
	mux.Handle("GET /api/live", audio.HandleLive(srv.PCMBus()))
	// Static dashboard assets (Pam's UI) at /
	webFS, _ := fs.Sub(web.Assets, ".")
	mux.Handle("GET /", http.FileServer(http.FS(webFS)))

	httpServer := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: mux,
	}

	go func() {
		log.Printf("HTTP API listening on %s", cfg.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	// Control listener + stream lifecycle is owned by the server.
	go func() {
		if err := srv.Start(); err != nil {
			log.Printf("server: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	srv.Close()
	log.Print("server stopped")
}

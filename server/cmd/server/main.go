// Package main is the entrypoint for the ESPMIC audio server.
//
// It wires the HTTP API (spec §15) onto a minimal net/http server with
// graceful shutdown on SIGINT/SIGTERM. Real module wiring (control
// sessions, RTP ingest, device/stream registries) lands in S1-S3.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"espmic/server/internal/api"
	"espmic/server/internal/config"
)

func main() {
	cfg := config.Load()

	mux := http.NewServeMux()
	api.RegisterRoutes(mux, cfg)

	srv := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("server listening on %s", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	log.Print("server stopped")
}

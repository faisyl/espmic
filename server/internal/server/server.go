// Package server holds the end-to-end wiring of the audio server (spec §3,
// §14, §15). It ties config -> persistence -> registries -> control sessions ->
// stream lifecycle -> RTP receiver -> decoder -> PCM bus -> recorder + live.
package server

import (
	"context"
	"database/sql"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"espmic/server/internal/audio"
	"espmic/server/internal/config"
	"espmic/server/internal/control"
	"espmic/server/internal/device"
	"espmic/server/internal/metrics"
	"espmic/server/internal/persistence"
	"espmic/server/internal/rtp"
	"espmic/server/internal/stream"
)

// Server is the top-level audio server (spec §3).
type Server struct {
	cfg     *config.Config
	db      *sql.DB
	device  *device.Registry
	stream  *stream.Registry
	metrics *metrics.Metrics
	rtp     *rtp.Receiver
	bus     *audio.PCMBus

	httpServer *http.Server
	controlLn  net.Listener

	ctx    context.Context
	cancel context.CancelFunc
}

// New builds a wired Server from a config, opening persistence and building
// registries (spec §3, §20).
func New(cfg *config.Config) (*Server, error) {
	ctx, cancel := context.WithCancel(context.Background())

	db, err := persistence.Open(cfg.DBPath)
	if err != nil {
		cancel()
		return nil, err
	}

	m := metrics.New()
	s := &Server{
		cfg:     cfg,
		db:      db,
		device:  device.NewRegistry(),
		stream:  stream.NewRegistry(),
		metrics: m,
		rtp:     rtp.NewReceiver(m),
		bus:     audio.NewPCMBus(),
		ctx:     ctx,
		cancel:  cancel,
	}
	return s, nil
}

// Start begins the control listener and returns when ctx is cancelled or an
// error occurs (spec §3). The HTTP API is owned by main.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.ControlAddr)
	if err != nil {
		return err
	}
	s.controlLn = ln

	go func() {
		log.Printf("control listening on %s", s.cfg.ControlAddr)
		s.controlLoop(ln)
	}()

	ctx, stop := signal.NotifyContext(s.ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	_ = ln.Close()
	s.cancel()
	return nil
}

func (s *Server) controlLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		sess := control.NewSession(conn, s, time.Now, func(msg control.Message) {
			log.Printf("control msg: %s", msg.Kind())
		})
		go func() {
			_ = sess.Run(s.ctx)
		}()
	}
}

// Authenticate implements control.Authenticator (spec §7, §19).
func (s *Server) Authenticate(ctx context.Context, deviceID, credential string) error {
	_, err := s.device.Get(deviceID)
	return err
}

// MetricsSurface returns the metrics snapshot for the HTTP endpoint (§18).
func (s *Server) MetricsSurface() interface{} {
	return s.metrics.Snapshot()
}

// DeviceList returns registered devices (§15).
func (s *Server) DeviceList() interface{} {
	return s.device.List()
}

func (s *Server) Close() error {
	s.cancel()
	if s.httpServer != nil {
		_ = s.httpServer.Close()
	}
	if s.controlLn != nil {
		_ = s.controlLn.Close()
	}
	return s.db.Close()
}

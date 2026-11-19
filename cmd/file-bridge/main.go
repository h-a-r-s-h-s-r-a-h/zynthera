// Command zynthera-file-bridge runs inside each guest, as the user's own
// account, and moves files in and out of their home directory. The host
// agent is its only client (spec section 14).
//
// Configuration, from the environment (written by cloud-init):
//
//	ZYN_BRIDGE_TOKEN          required; the secret the agent presents
//	ZYN_BRIDGE_PORT           port to listen on (default 7070)
//	ZYN_BRIDGE_MAX_UPLOAD_MB  largest upload accepted (default 0: no limit
//	                          here; the agent enforces the real one)
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"zynthera/internal/bridge"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("zynthera-file-bridge: %v", err)
	}
}

func run() error {
	token := os.Getenv("ZYN_BRIDGE_TOKEN")
	if token == "" {
		return errors.New("ZYN_BRIDGE_TOKEN is required")
	}
	port := 7070
	if raw := os.Getenv("ZYN_BRIDGE_PORT"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("ZYN_BRIDGE_PORT must be a port number")
		}
		port = n
	}
	var maxUpload int64
	if raw := os.Getenv("ZYN_BRIDGE_MAX_UPLOAD_MB"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			return errors.New("ZYN_BRIDGE_MAX_UPLOAD_MB must be a number")
		}
		maxUpload = n << 20
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	srv, err := bridge.New(home, token, maxUpload)
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              ":" + strconv.Itoa(port),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()
	log.Printf("zynthera-file-bridge: serving %s on port %d", home, port)
	if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

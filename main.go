package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// trafficRingSize is how many recent requests the admin dashboard's
// live feed retains and shows: it sizes the in-memory ring buffer (the
// SSE backlog) and, passed through to the page, caps the feed's rows.
const trafficRingSize = 200

func main() {
	startedAt := time.Now()
	port := getenv("ESSIE3_PORT", "9000")
	dataDir := getenv("ESSIE3_DATA_DIR", "./data")
	fallbackDataDir := getenv("ESSIE3_FALLBACK_DATA_DIR", "./fallback-data")
	adminPort := os.Getenv("ESSIE3_ADMIN_PORT")
	// Loopback by default (the admin surface is unauthenticated). Set to
	// 0.0.0.0 to reach it through a container's published port.
	adminHost := getenv("ESSIE3_ADMIN_HOST", "127.0.0.1")

	storage := NewStorage(dataDir)
	inlineExts := DefaultInlineExtensions
	if v, ok := os.LookupEnv("ESSIE3_FALLBACK_INLINE_EXTENSIONS"); ok {
		inlineExts = ParseExtList(v)
	}
	modeStr := os.Getenv("ESSIE3_FALLBACK_MODE")
	mode, err := ParseFallbackMode(modeStr)
	if err != nil {
		log.Fatalf("invalid ESSIE3_FALLBACK_MODE %q: %v", modeStr, err)
	}
	fallback, err := NewFallback(fallbackDataDir, inlineExts, mode)
	if err != nil {
		log.Fatalf("failed to load fallback data: %v", err)
	}

	auth := AuthConfig{
		AccessKey:      os.Getenv("ESSIE3_ACCESS_KEY"),
		FallbackPublic: os.Getenv("ESSIE3_FALLBACK_PUBLIC") == "true",
	}

	debug := os.Getenv("ESSIE3_DEBUG") == "true"

	fmt.Printf("essie3 starting on :%s\n", port)
	fmt.Printf("  data:     %s\n", dataDir)
	fmt.Printf("  fallback: %s (%d placeholders)\n", fallbackDataDir, fallback.Count())
	fmt.Printf("  fallback mode: %s\n", fallbackModeLabel(modeStr))
	fmt.Printf("  inline extensions: %s\n", strings.Join(inlineExts, ", "))
	if auth.Enabled() {
		fmt.Printf("  auth:     enabled (fallback_public=%v)\n", auth.FallbackPublic)
	} else {
		fmt.Printf("  auth:     disabled\n")
	}
	if debug {
		fmt.Printf("  debug:    enabled\n")
	}
	if adminPort != "" {
		fmt.Printf("  admin:    http://%s\n", net.JoinHostPort(adminHost, adminPort))
	}

	var handler http.Handler = NewHandler(storage, fallback, auth)
	var broker *TrafficBroker
	if adminPort != "" {
		broker = NewTrafficBroker(trafficRingSize)
		handler = WithTrafficCapture(handler, broker)
	}
	if debug {
		handler = WithDebugLogging(handler, os.Stderr)
	}
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	// Admin dashboard on its own loopback port. No WriteTimeout: it
	// would sever long-lived SSE connections.
	var adminSrv *http.Server
	if broker != nil {
		admin := NewAdminServer(storage, fallback, broker, startedAt, port, auth.Enabled())
		adminSrv = &http.Server{
			Addr:              net.JoinHostPort(adminHost, adminPort),
			Handler:           admin.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("admin server error: %v", err)
			}
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			log.Fatalf("server error: %v", err)
		}
	case <-ctx.Done():
		log.Println("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if adminSrv != nil {
			if err := adminSrv.Shutdown(shutdownCtx); err != nil {
				log.Printf("admin shutdown: %v", err)
			}
		}
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Fatalf("shutdown: %v", err)
		}
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func fallbackModeLabel(s string) string {
	if s == "" {
		return "prefer-pool (default)"
	}
	return s
}

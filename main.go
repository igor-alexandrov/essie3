package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	port := getenv("ESSIE3_PORT", "9000")
	dataDir := getenv("ESSIE3_DATA_DIR", "./data")
	fallbackDataDir := getenv("ESSIE3_FALLBACK_DATA_DIR", "./fallback-data")

	storage := NewStorage(dataDir)
	inlineExts := DefaultInlineExtensions
	if v, ok := os.LookupEnv("ESSIE3_FALLBACK_INLINE_EXTENSIONS"); ok {
		inlineExts = ParseExtList(v)
	}
	fallback, err := NewFallback(fallbackDataDir, inlineExts)
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
	fmt.Printf("  inline extensions: %s\n", strings.Join(inlineExts, ", "))
	if auth.Enabled() {
		fmt.Printf("  auth:     enabled (fallback_public=%v)\n", auth.FallbackPublic)
	} else {
		fmt.Printf("  auth:     disabled\n")
	}
	if debug {
		fmt.Printf("  debug:    enabled\n")
	}

	var handler http.Handler = NewHandler(storage, fallback, auth)
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

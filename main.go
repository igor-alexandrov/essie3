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
	port := getenv("PORT", "9000")
	dataDir := getenv("DATA_DIR", "./data")
	fallbackDataDir := getenv("FALLBACK_DATA_DIR", "./fallback-data")

	storage := NewStorage(dataDir)
	inlineExts := DefaultInlineExtensions
	if v, ok := os.LookupEnv("FALLBACK_INLINE_EXTENSIONS"); ok {
		inlineExts = ParseExtList(v)
	}
	fallback, err := NewFallback(fallbackDataDir, inlineExts)
	if err != nil {
		log.Fatalf("failed to load fallback data: %v", err)
	}

	fmt.Printf("essie3 starting on :%s\n", port)
	fmt.Printf("  data:     %s\n", dataDir)
	fmt.Printf("  fallback: %s (%d placeholders)\n", fallbackDataDir, fallback.Count())
	fmt.Printf("  inline extensions: %s\n", strings.Join(inlineExts, ", "))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           NewHandler(storage, fallback),
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

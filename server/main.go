package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/OCD-Labs/Weave/server/api"
	"github.com/OCD-Labs/Weave/server/db"
	"github.com/OCD-Labs/Weave/server/indexer"
	"github.com/OCD-Labs/Weave/server/prices"
)

func main() {
	// Load .env if present — silently ignored in production where env vars are injected directly.
	_ = godotenv.Load()

	dbPath := envOrDefault("DB_PATH", "./weave.db")
	rpcURL := mustEnv("ALCHEMY_RPC_URL")
	wsURL  := mustEnv("ALCHEMY_WS_URL")

	registryAddr    := mustEnv("WEAVE_REGISTRY_ADDRESS")
	apiPort         := envOrDefault("API_PORT", "8080")
	aiServicePort   := envOrDefault("AI_SERVICE_PORT", "3001")
	pollIntervalSec := envOrDefaultInt("PRICE_POLL_INTERVAL_SECS", 60)

	// Open (or create) the SQLite database and apply schema migrations.
	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the event indexer — subscribes to all registry and basket events.
	idx, err := indexer.New(ctx, wsURL, rpcURL, registryAddr, database)
	if err != nil {
		log.Fatalf("failed to start indexer: %v", err)
	}
	go idx.Run()

	// Start the price polling goroutine — reads mock oracles every pollIntervalSec.
	poller := prices.NewPoller(rpcURL, registryAddr, database, time.Duration(pollIntervalSec)*time.Second)
	go poller.Run(ctx)

	// Start the HTTP API server.
	aiBaseURL := envOrDefault("AI_BASE_URL", fmt.Sprintf("http://localhost:%s", aiServicePort))
	handler   := api.NewRouter(database, aiBaseURL)

	srv := &http.Server{
		Addr:         ":" + apiPort,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("Weave backend listening on :%s", apiPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Graceful shutdown on SIGINT or SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required environment variable %s is not set", key)
	}
	return v
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envOrDefaultInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
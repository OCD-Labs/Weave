package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/OCD-Labs/Weave/server/api"
	"github.com/OCD-Labs/Weave/server/db"
	"github.com/OCD-Labs/Weave/server/indexer"
	"github.com/OCD-Labs/Weave/server/nav"
	"github.com/OCD-Labs/Weave/server/prices"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	dbPath := envOrDefault("DB_PATH", "./weave.db")
	rpcURL := envOrDefault("RPC_URL", "https://rpc.testnet.chain.robinhood.com")
	wsURL := mustEnv("ALCHEMY_WS_URL")
	registryAddr := mustEnv("WEAVE_REGISTRY_ADDRESS")
	apiPort := envOrDefault("API_PORT", "8080")

	pollIntervalSec := envOrDefaultInt("PRICE_POLL_INTERVAL_SECS", 60)
	navIntervalSec := envOrDefaultInt("NAV_POLL_INTERVAL_SECS", 300)
	deployBlock := envOrDefaultInt64("DEPLOY_BLOCK", 65989689)

	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = database.Migrate()
	if err != nil {
		log.Fatalf("failed to migrate database: %v", err)
	}

	idx, err := indexer.New(ctx, wsURL, rpcURL, registryAddr, deployBlock, database)
	if err != nil {
		log.Fatalf("failed to start indexer: %v", err)
	}
	go idx.Run()

	poller := prices.NewPoller(rpcURL, registryAddr, database, time.Duration(pollIntervalSec)*time.Second)
	go poller.Run(ctx)

	navPoller := nav.NewPoller(rpcURL, database, time.Duration(navIntervalSec)*time.Second)
	go navPoller.Run(ctx)

	openAIKey := mustEnv("OPENAI_API_KEY")
	openAIModel := envOrDefault("OPENAI_MODEL", "gpt-4.1-mini")

	handler := api.NewRouter(database, openAIKey, openAIModel)

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

func envOrDefaultInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
	serverpkg "github.com/kiali/kiali-ai/kiali_ai_mcp/internal/server"
)

func main() {
	_ = godotenv.Load()
	addr := getEnv("SERVER_ADDR", ":8080")

	h := serverpkg.NewRouter()
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 15 * time.Second,
	}

	log.Printf("server listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server failed: %v", err)
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
} 
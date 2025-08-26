package server

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
)

func NewRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-API-Key"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Use(AuthMiddleware())
	// request logging
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			start := time.Now()
			next.ServeHTTP(w, req)
			log.Printf("%s %s %s", req.Method, req.URL.Path, time.Since(start))
		})
	})

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	r.Post("/v1/chat", ChatHandler)
	r.Post("/v1/ingest/kiali-docs", IngestKialiDocsHandler)
	r.Post("/v1/ingest/youtube", IngestYouTubeHandler)
	r.Post("/v1/admin/clean", CleanHandler)
	r.Post("/v1/admin/deduplicate", DeduplicateHandler)

	// Tools
	r.Get("/v1/tools/graph", GraphToolHandler)

	return r
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getContextWithTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	serverTimeout := 60 * time.Second
	if v := os.Getenv("SERVER_TIMEOUT_SECONDS"); v != "" {
		if d, err := time.ParseDuration(v + "s"); err == nil {
			serverTimeout = d
		}
	}
	return context.WithTimeout(parent, serverTimeout)
}

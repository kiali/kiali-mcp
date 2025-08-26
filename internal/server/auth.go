package server

import (
	"encoding/base64"
	"net/http"
	"os"
	"strings"
)

func AuthMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" {
				next.ServeHTTP(w, r)
				return
			}

			// API key header
			apiKey := r.Header.Get("X-API-Key")
			expected := os.Getenv("API_KEY")
			if expected != "" && apiKey == expected {
				next.ServeHTTP(w, r)
				return
			}

			// Basic auth header
			auth := r.Header.Get("Authorization")
			if strings.HasPrefix(auth, "Basic ") {
				payload, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic "))
				parts := strings.SplitN(string(payload), ":", 2)
				if len(parts) == 2 {
					userEnv := os.Getenv("BASIC_AUTH_USER")
					passEnv := os.Getenv("BASIC_AUTH_PASS")
					if parts[0] == userEnv && parts[1] == passEnv && userEnv != "" {
						next.ServeHTTP(w, r)
						return
					}
				}
			}

			w.Header().Set("WWW-Authenticate", "Basic realm=restricted")
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		})
	}
} 
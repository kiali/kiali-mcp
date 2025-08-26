package server

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/kiali/kiali-ai/kiali_ai_mcp/internal/config"
	"github.com/kiali/kiali-ai/kiali_ai_mcp/internal/rag"
)

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":       msg,
		"status_code": status,
	})
}

type chatRequest struct {
	Query   string `json:"query"`
	Context any    `json:"context,omitempty"`
}

type chatResponse struct {
	Answer     string               `json:"answer"`
	Citations  []rag.Citation       `json:"citations"`
	UsedModels rag.ModelIdentifiers `json:"used_models"`
}

func ChatHandler(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	ctx, cancel := getContextWithTimeout(r.Context())
	defer cancel()

	answer, citations, models, err := rag.DefaultEngine().Answer(ctx, req.Query, req.Context)
	if err != nil {
		log.Printf("%s %s error: %v", r.Method, r.URL.Path, err)
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(chatResponse{Answer: answer, Citations: citations, UsedModels: models})
}

type ingestDocsRequest struct {
	BaseURL string `json:"base_url"`
}

func IngestKialiDocsHandler(w http.ResponseWriter, r *http.Request) {
	var req ingestDocsRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.BaseURL == "" {
		req.BaseURL = "https://kiali.io/"
	}
	ctx, cancel := getContextWithTimeout(r.Context())
	defer cancel()
	ingested, skipped, err := rag.DefaultEngine().IngestKialiDocs(ctx, req.BaseURL)
	if err != nil {
		log.Printf("%s %s error: %v", r.Method, r.URL.Path, err)
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ingested": ingested, "skipped": skipped})
}

type ingestYouTubeRequest struct {
	ChannelOrPlaylistURL string `json:"channel_or_playlist_url"`
}

func IngestYouTubeHandler(w http.ResponseWriter, r *http.Request) {
	var req ingestYouTubeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ChannelOrPlaylistURL == "" {
		writeJSONError(w, http.StatusBadRequest, "channel_or_playlist_url required")
		return
	}
	ctx, cancel := getContextWithTimeout(r.Context())
	defer cancel()
	ingested, skipped, err := rag.DefaultEngine().IngestYouTube(ctx, req.ChannelOrPlaylistURL)
	if err != nil {
		log.Printf("%s %s error: %v", r.Method, r.URL.Path, err)
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ingested": ingested, "skipped": skipped})
}

func CleanHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := getContextWithTimeout(r.Context())
	defer cancel()
	removed, err := rag.DefaultEngine().Clean(ctx)
	if err != nil {
		log.Printf("%s %s error: %v", r.Method, r.URL.Path, err)
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"removed_documents": removed})
}

func DeduplicateHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := getContextWithTimeout(r.Context())
	defer cancel()
	removed, err := rag.DefaultEngine().Deduplicate(ctx)
	if err != nil {
		log.Printf("%s %s error: %v", r.Method, r.URL.Path, err)
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"removed_duplicates": removed})
}

// GraphToolHandler fetches Kiali graph JSON from an internal Kiali API and then
// invokes the chat engine with a purpose-built prompt and the JSON as context.
// Configure via:
// - KIALI_API_BASE (required): e.g. https://kiali-istio-system.apps-crc.testing
// - KIALI_BEARER_TOKEN (optional): bearer token for upstream
func GraphToolHandler(w http.ResponseWriter, r *http.Request) {
	base := config.Get("KIALI_API_BASE", "")
	if base == "" {
		writeJSONError(w, http.StatusBadRequest, "KIALI_API_BASE not configured")
		return
	}

	// Build Kiali /api/namespaces/graph URL with incoming query params
	u, err := url.Parse(base)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "invalid KIALI_API_BASE")
		return
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/namespaces/graph"
	q := u.Query()
	for key, vals := range r.URL.Query() {
		for _, v := range vals {
			q.Add(key, v)
		}
	}
	u.RawQuery = q.Encode()

	ctx, cancel := getContextWithTimeout(r.Context())
	defer cancel()
	log.Printf("graph tool: requesting Kiali API %s", u.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if token := config.Get("KIALI_BEARER_TOKEN", ""); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")

	// Build HTTP client with optional TLS settings
	transport := &http.Transport{}
	tlsConfig := &tls.Config{}
	if strings.EqualFold(config.Get("KIALI_TLS_INSECURE", ""), "true") || config.Get("KIALI_TLS_INSECURE", "") == "1" || strings.EqualFold(config.Get("KIALI_TLS_INSECURE", ""), "yes") || strings.EqualFold(config.Get("KIALI_TLS_INSECURE", ""), "on") {
		log.Printf("graph tool: TLS verification disabled for Kiali API (KIALI_TLS_INSECURE)")
		tlsConfig.InsecureSkipVerify = true
	}
	if tlsConfig.InsecureSkipVerify == false {
		if caFile := config.Get("KIALI_CA_FILE", ""); caFile != "" {
			pem, err := os.ReadFile(caFile)
			if err != nil {
				log.Printf("graph tool: failed to read KIALI_CA_FILE: %v", err)
			} else {
				pool, _ := x509.SystemCertPool()
				if pool == nil {
					pool = x509.NewCertPool()
				}
				if ok := pool.AppendCertsFromPEM(pem); !ok {
					log.Printf("graph tool: no certs appended from KIALI_CA_FILE")
				}
				tlsConfig.RootCAs = pool
				log.Printf("graph tool: using custom CA bundle from %s", caFile)
			}
		}
	}
	transport.TLSClientConfig = tlsConfig
	client := &http.Client{Transport: transport}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("graph tool: Kiali API request error: %v", err)
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		log.Printf("graph tool: Kiali API non-200 status=%d body_len=%d", resp.StatusCode, len(b))
		writeJSONError(w, http.StatusBadGateway, string(b))
		return
	}

	var graphJSON any
	if err := json.NewDecoder(resp.Body).Decode(&graphJSON); err != nil {
		log.Printf("graph tool: failed to decode Kiali API JSON: %v", err)
		writeJSONError(w, http.StatusBadGateway, "failed to decode Kiali graph JSON")
		return
	}
	log.Printf("graph tool: Kiali API response decoded successfully")

	prompt := "You are an expert in Kubernetes, Istio, and Kiali. Analyze the following JSON graph data used by Kiali to render a service mesh. Summarize the main services and traffic flows, highlight anomalies (errors, unhealthy workloads, missing connections), and suggest troubleshooting steps or optimizations. JSON:"

	ctx2, cancel2 := getContextWithTimeout(r.Context())
	defer cancel2()
	log.Printf("graph tool: invoking AI analysis")
	answer, citations, models, err := rag.DefaultEngine().Answer(ctx2, prompt, graphJSON)
	if err != nil {
		log.Printf("graph tool: AI analysis error: %v", err)
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("graph tool: AI analysis complete")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"answer":      answer,
		"citations":   citations,
		"used_models": models,
		"graph_query": u.String(),
	})
}

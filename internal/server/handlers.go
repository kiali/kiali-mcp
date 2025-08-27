package server

import (
	"encoding/json"
	"log"
	"net/http"

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

package rag

import (
	"context"
	"sync"
)

type Engine interface {
	Answer(ctx context.Context, query string, kialiContext any) (answer string, citations []Citation, models ModelIdentifiers, err error)
	IngestKialiDocs(ctx context.Context, baseURL string) (ingested int, skipped int, err error)
	IngestYouTube(ctx context.Context, channelOrPlaylistURL string) (ingested int, skipped int, err error)
	Clean(ctx context.Context) (removedDocuments int, err error)
	Deduplicate(ctx context.Context) (removedDuplicates int, err error)
}

type ModelIdentifiers struct {
	CompletionModel string `json:"completion_model"`
	EmbeddingModel  string `json:"embedding_model"`
}

type Citation struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	Span  string `json:"span"`
}

var (
	defaultOnce sync.Once
	defaultEng  Engine
)

func DefaultEngine() Engine {
	defaultOnce.Do(func() {
		defaultEng = NewEngine()
	})
	return defaultEng
}

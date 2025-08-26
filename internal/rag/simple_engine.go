package rag

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/kiali/kiali-ai/kiali_ai_mcp/internal/config"
	pgvector "github.com/pgvector/pgvector-go"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type extractedSection struct {
	Title   string
	ID      string
	Content string
	URL     string
}

type engine struct {
	// Gemini configuration
	apiKey string
	models ModelIdentifiers

	db           *sql.DB
	httpClient   *http.Client
	backend      string // "sqlite" or "postgres"
	embeddingDim int
}

func NewEngine() Engine {
	// Provider selection and model defaults
	provider := strings.ToLower(config.Get("LLM_PROVIDER", "gemini"))
	compDef := "gemini-1.5-flash"
	embDef := "text-embedding-004"
	defEmbDim := 1536
	if provider == "openai" {
		compDef = "gpt-4o-mini"
		embDef = "text-embedding-3-small"
		defEmbDim = 1536
	}
	completionModel := config.Get("COMPLETION_MODEL", compDef)
	embeddingModel := config.Get("EMBEDDING_MODEL", embDef)
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}

	backend := strings.ToLower(config.Get("VECTOR_BACKEND", "sqlite"))
	embDim := defEmbDim
	if v := config.Get("EMBEDDING_DIM", ""); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			embDim = i
		}
	}

	var db *sql.DB
	var err error
	if backend == "postgres" {
		dsn := buildPostgresDSN()
		db, err = sql.Open("pgx", dsn)
		if err != nil {
			log.Fatalf("open postgres: %v", err)
		}
		if err := initPostgres(db, embDim); err != nil {
			log.Fatalf("init postgres schema: %v", err)
		}
	} else {
		dbPath := os.Getenv("VECTOR_DB_PATH")
		if dbPath == "" {
			dbPath = "./data/rag.sqlite"
		}
		if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				log.Fatalf("create db dir: %v", err)
			}
		}
		db, err = sql.Open("sqlite", dbPath)
		if err != nil {
			log.Fatalf("open sqlite: %v", err)
		}
		_, _ = db.Exec("PRAGMA journal_mode=WAL;")
		_, _ = db.Exec("PRAGMA synchronous=NORMAL;")
		_, _ = db.Exec("PRAGMA busy_timeout=5000;")
		if err := initSqlite(db); err != nil {
			log.Fatalf("init sqlite schema: %v", err)
		}
	}

	return &engine{
		apiKey:       apiKey,
		models:       ModelIdentifiers{CompletionModel: completionModel, EmbeddingModel: embeddingModel},
		db:           db,
		httpClient:   &http.Client{Timeout: 20 * time.Second},
		backend:      backend,
		embeddingDim: embDim,
	}
}

func (e *engine) Answer(ctx context.Context, query string, kialiContext any) (string, []Citation, ModelIdentifiers, error) {
	if strings.TrimSpace(query) == "" {
		return "", nil, e.models, errors.New("empty query")
	}
	emb, err := e.embed(ctx, query)
	if err != nil {
		return "", nil, e.models, err
	}
	docs, err := e.search(ctx, emb, 8)
	if err != nil {
		return "", nil, e.models, err
	}

	prompt := buildPrompt(query, kialiContext, docs)
	answer, err := e.complete(ctx, prompt)
	if err != nil {
		return "", nil, e.models, err
	}
	cit := make([]Citation, 0, len(docs))
	for _, d := range docs {
		cit = append(cit, Citation{Title: d.Title, URL: d.URL, Span: d.Snippet})
	}
	return answer, cit, e.models, nil
}

func (e *engine) IngestKialiDocs(ctx context.Context, base string) (int, int, error) {
	u, err := url.Parse(base)
	if err != nil {
		return 0, 0, err
	}
	if u.Scheme == "" {
		u.Scheme = "https"
	}
	if u.Host == "" {
		u.Host = "kiali.io"
	}

	visited := map[string]bool{}
	queue := []string{u.String()}
	ingested, skipped := 0, 0
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		if visited[curr] {
			continue
		}
		visited[curr] = true
		if !strings.Contains(curr, "kiali.io") {
			continue
		}

		doc, err := e.fetchDoc(curr)
		if err != nil {
			continue
		}
		sections := extractKialiSections(doc, curr)
		for _, sec := range sections {
			if len(strings.TrimSpace(sec.Content)) < 10 {
				continue
			}
			exists, _ := e.documentExists(ctx, sec.URL)
			if exists {
				skipped++
				continue
			}
			upErr := e.upsertDocument(ctx, sec.Title, sec.URL, sec.Content)
			if upErr != nil {
				log.Printf("upsert error: %v", upErr)
				continue
			}
			ingested++
		}

		for _, link := range collectKialiLinks(doc, curr) {
			if strings.Contains(link, "kiali.io") && !visited[link] && shouldCrawl(link) {
				queue = append(queue, link)
			}
		}
	}
	return ingested, skipped, nil
}

func (e *engine) IngestYouTube(ctx context.Context, channelOrPlaylistURL string) (int, int, error) {
	if !strings.Contains(channelOrPlaylistURL, "http") {
		return 0, 0, errors.New("expect URLs or use external ingestion pipeline")
	}
	// If a single playlist URL is given, expand to video URLs
	urlsStr := strings.Split(channelOrPlaylistURL, ",")
	var urls []string
	for _, s := range urlsStr {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		urls = append(urls, s)
	}

	expanded := make([]string, 0, len(urls))
	for _, u := range urls {
		if isYouTubePlaylistURL(u) {
			vs, err := e.expandPlaylist(ctx, u)
			if err != nil {
				log.Printf("playlist expand error: %v", err)
			} else {
				expanded = append(expanded, vs...)
			}
		} else {
			expanded = append(expanded, u)
		}
	}
	// Deduplicate expanded URLs
	seen := map[string]bool{}
	final := make([]string, 0, len(expanded))
	for _, v := range expanded {
		if !seen[v] {
			seen[v] = true
			final = append(final, v)
		}
	}

	ingested, skipped := 0, 0
	for _, u := range final {
		exists, _ := e.documentExists(ctx, u)
		if exists {
			skipped++
			continue
		}
		body, err := e.fetchRaw(u)
		if err != nil || len(body) < 200 {
			continue
		}
		if err := e.upsertDocument(ctx, "YouTube Video", u, body); err == nil {
			ingested++
		}
	}
	return ingested, skipped, nil
}

func isYouTubePlaylistURL(u string) bool {
	return strings.Contains(u, "youtube.com/playlist") || (strings.Contains(u, "list=") && strings.Contains(u, "youtube.com"))
}

func (e *engine) expandPlaylist(ctx context.Context, playlistURL string) ([]string, error) {
	// Prefer Data API if key available
	apiKey := os.Getenv("YOUTUBE_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}
	listID := extractPlaylistID(playlistURL)
	if listID != "" && apiKey != "" {
		videos, err := e.expandPlaylistViaAPI(ctx, apiKey, listID)
		if err == nil && len(videos) > 0 {
			return videos, nil
		}
		log.Printf("fallback to HTML playlist parse: %v", err)
	}
	// Fallback: parse HTML
	doc, err := e.fetchDoc(playlistURL)
	if err != nil {
		return nil, err
	}
	urls := []string{}
	doc.Find("a").Each(func(_ int, s *goquery.Selection) {
		href, ok := s.Attr("href")
		if !ok {
			return
		}
		if strings.Contains(href, "/watch?") && strings.Contains(href, "v=") {
			abs := resolveURL(playlistURL, href)
			urls = append(urls, normalizeYouTubeWatchURL(abs))
		}
	})
	// dedupe
	seen := map[string]bool{}
	out := []string{}
	for _, v := range urls {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out, nil
}

func (e *engine) expandPlaylistViaAPI(ctx context.Context, apiKey, playlistID string) ([]string, error) {
	base := "https://www.googleapis.com/youtube/v3/playlistItems"
	pageToken := ""
	var results []string
	client := e.httpClient
	for {
		q := url.Values{}
		q.Set("part", "contentDetails")
		q.Set("playlistId", playlistID)
		q.Set("maxResults", "50")
		q.Set("key", apiKey)
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		endpoint := base + "?" + q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("yt api %d: %s", resp.StatusCode, string(b))
		}
		var out struct {
			Items []struct {
				ContentDetails struct {
					VideoId string `json:"videoId"`
				} `json:"contentDetails"`
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()
		for _, it := range out.Items {
			if it.ContentDetails.VideoId != "" {
				results = append(results, "https://www.youtube.com/watch?v="+it.ContentDetails.VideoId)
			}
		}
		if out.NextPageToken == "" {
			break
		}
		pageToken = out.NextPageToken
	}
	return results, nil
}

func extractPlaylistID(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	if id := parsed.Query().Get("list"); id != "" {
		return id
	}
	return ""
}

func normalizeYouTubeWatchURL(src string) string {
	u, err := url.Parse(src)
	if err != nil {
		return src
	}
	if strings.Contains(u.Path, "/embed/") {
		id := strings.TrimPrefix(u.Path, "/embed/")
		return "https://www.youtube.com/watch?v=" + id
	}
	return src
}

func (e *engine) Deduplicate(ctx context.Context) (int, error) {
	removed := 0
	if e.backend == "postgres" {
		// find duplicate urls keeping min(id)
		rows, err := e.db.QueryContext(ctx, `
			SELECT id FROM documents d
			WHERE EXISTS (
			  SELECT 1 FROM documents d2
			  WHERE d2.url = d.url AND d2.id < d.id
			)
		`)
		if err != nil {
			return 0, err
		}
		defer rows.Close()
		var dupIDs []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err == nil {
				dupIDs = append(dupIDs, id)
			}
		}
		for _, id := range dupIDs {
			if _, err := e.db.ExecContext(ctx, "DELETE FROM embeddings WHERE document_id=$1", id); err != nil {
				return removed, err
			}
			res, err := e.db.ExecContext(ctx, "DELETE FROM documents WHERE id=$1", id)
			if err != nil {
				return removed, err
			}
			af, _ := res.RowsAffected()
			removed += int(af)
		}
		return removed, nil
	}
	// sqlite
	rows, err := e.db.QueryContext(ctx, `
		SELECT id FROM documents d
		WHERE EXISTS (
		  SELECT 1 FROM documents d2
		  WHERE d2.url = d.url AND d2.id < d.id
		)
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var dupIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			dupIDs = append(dupIDs, id)
		}
	}
	for _, id := range dupIDs {
		if _, err := e.db.ExecContext(ctx, "DELETE FROM embeddings WHERE document_id=?", id); err != nil {
			return removed, err
		}
		res, err := e.db.ExecContext(ctx, "DELETE FROM documents WHERE id=?", id)
		if err != nil {
			return removed, err
		}
		af, _ := res.RowsAffected()
		removed += int(af)
	}
	return removed, nil
}

func (e *engine) documentExists(ctx context.Context, url string) (bool, error) {
	var count int
	if e.backend == "postgres" {
		err := e.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM documents WHERE url=$1", url).Scan(&count)
		return count > 0, err
	}
	err := e.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM documents WHERE url=?", url).Scan(&count)
	return count > 0, err
}

func (e *engine) Clean(ctx context.Context) (int, error) {
	// Return number of removed documents; embeddings have FK delete cascade not defined, so delete embeddings first
	var removed int
	if e.backend == "postgres" {
		if _, err := e.db.ExecContext(ctx, "DELETE FROM embeddings"); err != nil {
			return 0, err
		}
		res, err := e.db.ExecContext(ctx, "DELETE FROM documents")
		if err != nil {
			return 0, err
		}
		affected, _ := res.RowsAffected()
		removed = int(affected)
		return removed, nil
	}
	if _, err := e.db.ExecContext(ctx, "DELETE FROM embeddings"); err != nil {
		return 0, err
	}
	res, err := e.db.ExecContext(ctx, "DELETE FROM documents")
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
	removed = int(affected)
	return removed, nil
}

// --- storage backends ---

type docChunk struct {
	ID      int64
	Title   string
	URL     string
	Snippet string
	Content string
	Vector  []float32
}

func initSqlite(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS documents (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	title TEXT,
	url TEXT,
	content TEXT
);
CREATE TABLE IF NOT EXISTS embeddings (
	document_id INTEGER,
	position INTEGER,
	vector BLOB,
	snippet TEXT,
	FOREIGN KEY(document_id) REFERENCES documents(id)
);
CREATE INDEX IF NOT EXISTS idx_embeddings_doc ON embeddings(document_id);
`)
	return err
}

func initPostgres(db *sql.DB, dim int) error {
	_, err := db.Exec(`CREATE EXTENSION IF NOT EXISTS vector;`)
	if err != nil {
		return err
	}
	ddl := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS documents (
	id BIGSERIAL PRIMARY KEY,
	title TEXT,
	url TEXT,
	content TEXT
);
CREATE TABLE IF NOT EXISTS embeddings (
	document_id BIGINT REFERENCES documents(id),
	position INTEGER,
	vector VECTOR(%d),
	snippet TEXT
);
CREATE INDEX IF NOT EXISTS idx_embeddings_doc ON embeddings(document_id);
`, dim)
	_, err = db.Exec(ddl)
	return err
}

func (e *engine) upsertDocument(ctx context.Context, title, docURL, content string) error {
	chunks := splitIntoChunks(content, 800)
	if e.backend == "postgres" {
		var id int64
		if err := e.db.QueryRowContext(ctx, "INSERT INTO documents(title, url, content) VALUES($1,$2,$3) RETURNING id", title, docURL, content).Scan(&id); err != nil {
			return err
		}
		for i, ch := range chunks {
			emb, err := e.embed(ctx, ch)
			if err != nil {
				return err
			}
			snippet := ch[:min(160, len(ch))]
			vec := pgvector.NewVector(emb)
			if _, err := e.db.ExecContext(ctx, "INSERT INTO embeddings(document_id, position, vector, snippet) VALUES($1,$2,$3,$4)", id, i, vec, snippet); err != nil {
				return err
			}
		}
		return nil
	}
	// sqlite path
	res, err := e.db.ExecContext(ctx, "INSERT INTO documents(title, url, content) VALUES(?,?,?)", title, docURL, content)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	for i, ch := range chunks {
		emb, err := e.embed(ctx, ch)
		if err != nil {
			return err
		}
		snippet := ch[:min(160, len(ch))]
		if _, err := e.db.ExecContext(ctx, "INSERT INTO embeddings(document_id, position, vector, snippet) VALUES(?,?,?,?)", id, i, floatsToBlob(emb), snippet); err != nil {
			return err
		}
	}
	return nil
}

func (e *engine) search(ctx context.Context, queryVec []float32, k int) ([]docChunk, error) {
	if e.backend == "postgres" {
		q := "SELECT d.id, d.title, d.url, e.snippet FROM embeddings e JOIN documents d ON d.id=e.document_id ORDER BY e.vector <=> $1 LIMIT $2"
		rows, err := e.db.QueryContext(ctx, q, pgvector.NewVector(queryVec), k)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var results []docChunk
		for rows.Next() {
			var id int64
			var title, u, snippet string
			if err := rows.Scan(&id, &title, &u, &snippet); err != nil {
				continue
			}
			results = append(results, docChunk{ID: id, Title: title, URL: u, Snippet: snippet})
		}
		return results, nil
	}
	// sqlite brute force
	rows, err := e.db.QueryContext(ctx, "SELECT d.id, d.title, d.url, e.snippet, e.vector FROM embeddings e JOIN documents d ON d.id = e.document_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []docChunk
	for rows.Next() {
		var id int64
		var title, u, snippet string
		var blob []byte
		if err := rows.Scan(&id, &title, &u, &snippet, &blob); err != nil {
			continue
		}
		vec := blobToFloats(blob)
		sim := cosine(vec, queryVec)
		results = append(results, docChunk{ID: id, Title: title, URL: u, Snippet: fmt.Sprintf("%s (sim=%.3f)", snippet, sim), Vector: vec})
	}
	if len(results) > k {
		results = topK(results, k)
	}
	return results, nil
}

// --- LLM + web helpers remain unchanged ---

func (e *engine) embed(ctx context.Context, text string) ([]float32, error) {
	provider := strings.ToLower(config.Get("LLM_PROVIDER", "gemini"))
	if provider == "openai" {
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return nil, errors.New("OPENAI_API_KEY not set")
		}
		model := e.models.EmbeddingModel
		if model == "" {
			model = "text-embedding-3-small"
		}
		endpoint := "https://api.openai.com/v1/embeddings"
		body := map[string]any{
			"model": model,
			"input": text,
		}
		bs, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bs))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := e.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("embed status %d: %s", resp.StatusCode, string(b))
		}
		var out struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, err
		}
		if len(out.Data) == 0 || len(out.Data[0].Embedding) == 0 {
			return nil, errors.New("empty embedding values")
		}
		vec := make([]float32, len(out.Data[0].Embedding))
		for i, v := range out.Data[0].Embedding {
			vec[i] = float32(v)
		}
		return vec, nil
	}
	// default: Gemini
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		return nil, errors.New("GEMINI_API_KEY not set")
	}
	model := e.models.EmbeddingModel
	if model == "" {
		model = "text-embedding-004"
	}
	endpoint := fmt.Sprintf("https://generativelanguage.googleapis.com/v1/models/%s:embedContent?key=%s", model, key)
	body := map[string]any{
		"model":   "models/" + model,
		"content": map[string]any{"parts": []map[string]any{{"text": text}}},
	}
	bs, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bs))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embed status %d: %s", resp.StatusCode, string(b))
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	embObj, ok := out["embedding"].(map[string]any)
	if !ok {
		return nil, errors.New("no embedding in response")
	}
	vals, ok := embObj["values"].([]any)
	if !ok || len(vals) == 0 {
		return nil, errors.New("empty embedding values")
	}
	vec := make([]float32, len(vals))
	for i, v := range vals {
		vec[i] = float32(v.(float64))
	}
	return vec, nil
}

func (e *engine) complete(ctx context.Context, prompt string) (string, error) {
	provider := strings.ToLower(getEnv("LLM_PROVIDER", "gemini"))
	if provider == "openai" {
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return "", errors.New("OPENAI_API_KEY not set")
		}
		model := e.models.CompletionModel
		if model == "" {
			model = "gpt-4o-mini"
		}
		endpoint := "https://api.openai.com/v1/chat/completions"
		body := map[string]any{
			"model":       model,
			"temperature": 0.2,
			"max_tokens":  1024,
			"messages": []map[string]any{
				{"role": "system", "content": systemPrompt},
				{"role": "user", "content": prompt},
			},
		}
		bs, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bs))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := e.httpClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			return "", fmt.Errorf("complete status %d: %s", resp.StatusCode, string(b))
		}
		var out struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return "", err
		}
		if len(out.Choices) == 0 {
			return "", errors.New("no choices in response")
		}
		return out.Choices[0].Message.Content, nil
	}
	// default: Gemini
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		return "", errors.New("GEMINI_API_KEY not set")
	}
	model := e.models.CompletionModel
	if model == "" {
		model = "gemini-1.5-flash"
	}
	endpoint := fmt.Sprintf("https://generativelanguage.googleapis.com/v1/models/%s:generateContent?key=%s", model, key)
	body := map[string]any{
		"contents": []map[string]any{{
			"parts": []map[string]any{{"text": systemPrompt + "\n\n" + prompt}},
		}},
		"generationConfig": map[string]any{"maxOutputTokens": 1024, "temperature": 0.2},
	}
	bs, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bs))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("complete status %d: %s", resp.StatusCode, string(b))
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	cands, ok := out["candidates"].([]any)
	if !ok || len(cands) == 0 {
		return "", errors.New("no candidates")
	}
	content, ok := cands[0].(map[string]any)["content"].(map[string]any)
	if !ok {
		return "", errors.New("no content in candidate")
	}
	parts, ok := content["parts"].([]any)
	if !ok || len(parts) == 0 {
		return "", errors.New("no parts in content")
	}
	text, _ := parts[0].(map[string]any)["text"].(string)
	return text, nil
}

const systemPrompt = "You are Kiali/Istio assistant. Be precise, cite sources, and use provided Kiali endpoint data to analyze graphs, traffic, metrics, and propose troubleshooting steps."

func buildPrompt(query string, kialiContext any, docs []docChunk) string {
	var b strings.Builder
	b.WriteString("User question:\n")
	b.WriteString(query)
	b.WriteString("\n\nRelevant context (from Kiali docs and demos):\n")
	for i, d := range docs {
		b.WriteString(fmt.Sprintf("[%d] %s - %s: %s\n", i+1, d.Title, d.URL, d.Snippet))
	}
	if kialiContext != nil {
		b.WriteString("\nKiali data (graphs/metrics JSON):\n")
		bs, _ := json.Marshal(kialiContext)
		b.Write(bs)
	}
	b.WriteString("\nAnswer step-by-step. Reference sources by URL when relevant.")
	return b.String()
}

// --- web fetching helpers ---

func (e *engine) fetchDoc(u string) (*goquery.Document, error) {
	resp, err := e.httpClient.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return goquery.NewDocumentFromReader(resp.Body)
}

func (e *engine) fetchRaw(u string) (string, error) {
	resp, err := e.httpClient.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

// extractKialiContent builds structured text from typical kiali.io docs markup, prioritizing <h3 id> FAQ sections,
// and otherwise <h2> sections with following paragraphs.
func extractKialiContent(doc *goquery.Document, currURL string) (string, string) {
	root := doc.Find(".td-content")
	if root.Length() == 0 {
		root = doc.Find("main")
	}
	if root.Length() == 0 {
		root = doc.Find("article")
	}
	title := strings.TrimSpace(root.Find("h1").First().Text())
	if title == "" {
		title = strings.TrimSpace(doc.Find("title").Text())
	}
	var b strings.Builder
	if title != "" {
		b.WriteString(title)
		b.WriteString("\n\n")
	}

	// Prefer FAQ-style sections: <h3 id="...">Question</h3> followed by <p> until next <h3>
	faqSections := root.Find("h3[id]")
	if faqSections.Length() > 0 {
		faqSections.Each(func(_ int, h3 *goquery.Selection) {
			sec := strings.TrimSpace(h3.Text())
			id, _ := h3.Attr("id")
			if sec == "" {
				return
			}
			b.WriteString("### ")
			b.WriteString(sec)
			if id != "" {
				b.WriteString(" (section #")
				b.WriteString(id)
				b.WriteString(")")
			}
			b.WriteString("\n")
			for sib := h3.Next(); sib.Length() > 0; sib = sib.Next() {
				tag := goquery.NodeName(sib)
				if tag == "h3" {
					break
				}
				if tag == "p" {
					text := strings.TrimSpace(sib.Text())
					if text != "" {
						b.WriteString(text)
						b.WriteString("\n\n")
					}
				}
			}
		})
		content := strings.TrimSpace(b.String())
		return title, content
	}

	// Fallback: <h2> sections with following paragraphs until next <h2>
	root.Find("h2").Each(func(_ int, h2 *goquery.Selection) {
		sec := strings.TrimSpace(h2.Text())
		id, _ := h2.Attr("id")
		if sec == "" {
			return
		}
		b.WriteString("## ")
		b.WriteString(sec)
		if id != "" {
			b.WriteString(" (section #")
			b.WriteString(id)
			b.WriteString(")")
		}
		b.WriteString("\n")
		for sib := h2.Next(); sib.Length() > 0; sib = sib.Next() {
			tag := goquery.NodeName(sib)
			if tag == "h2" {
				break
			}
			if tag == "p" {
				text := strings.TrimSpace(sib.Text())
				if text != "" {
					b.WriteString(text)
					b.WriteString("\n\n")
				}
			}
		}
	})
	content := strings.TrimSpace(b.String())
	return title, content
}

func normalizeYouTubeEmbed(src string) string {
	u, err := url.Parse(src)
	if err != nil {
		return src
	}
	if strings.Contains(u.Path, "/embed/") {
		id := strings.TrimPrefix(u.Path, "/embed/")
		return "https://www.youtube.com/watch?v=" + id
	}
	return src
}

func collectKialiLinks(doc *goquery.Document, curr string) []string {
	root := doc.Find(".td-content")
	if root.Length() == 0 {
		root = doc.Find("main")
	}
	var out []string
	root.Find("a").Each(func(_ int, s *goquery.Selection) {
		href, ok := s.Attr("href")
		if !ok || href == "" {
			return
		}
		if strings.HasPrefix(href, "#") {
			return
		}
		if strings.HasPrefix(href, "mailto:") || strings.HasPrefix(href, "javascript:") {
			return
		}
		link := resolveURL(curr, href)
		if shouldCrawl(link) {
			out = append(out, link)
		}
	})
	return out
}

func shouldCrawl(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	if parsed.Host == "" {
		return false
	}
	if !strings.Contains(parsed.Host, "kiali.io") {
		return false
	}
	// focus on docs subtree
	if !strings.Contains(parsed.Path, "/docs/") {
		return false
	}
	// skip assets and binary files
	lower := strings.ToLower(parsed.Path)
	if strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".gif") || strings.HasSuffix(lower, ".svg") || strings.HasSuffix(lower, ".ico") || strings.HasSuffix(lower, ".pdf") || strings.HasSuffix(lower, ".zip") {
		return false
	}
	// avoid taxonomy pages
	if strings.Contains(lower, "/tag/") || strings.Contains(lower, "/category/") {
		return false
	}
	return true
}

func resolveURL(baseURL, href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if u.IsAbs() {
		return u.String()
	}
	b, _ := url.Parse(baseURL)
	return b.ResolveReference(u).String()
}

// --- vector math and utils ---

func splitIntoChunks(text string, wordsPerChunk int) []string {
	words := strings.Fields(text)
	var chunks []string
	for i := 0; i < len(words); i += wordsPerChunk {
		end := i + wordsPerChunk
		if end > len(words) {
			end = len(words)
		}
		chunks = append(chunks, strings.Join(words[i:end], " "))
	}
	return chunks
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func floatsToBlob(vec []float32) []byte {
	b := make([]byte, len(vec)*4)
	for i, f := range vec {
		bits := math.Float32bits(f)
		b[i*4+0] = byte(bits)
		b[i*4+1] = byte(bits >> 8)
		b[i*4+2] = byte(bits >> 16)
		b[i*4+3] = byte(bits >> 24)
	}
	return b
}

func blobToFloats(b []byte) []float32 {
	if len(b)%4 != 0 {
		return nil
	}
	out := make([]float32, len(b)/4)
	for i := 0; i < len(out); i++ {
		bits := uint32(b[i*4+0]) | uint32(b[i*4+1])<<8 | uint32(b[i*4+2])<<16 | uint32(b[i*4+3])<<24
		out[i] = math.Float32frombits(bits)
	}
	return out
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		na += av * av
		nb += bv * bv
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func topK(items []docChunk, k int) []docChunk {
	res := make([]docChunk, 0, k)
	for i := 0; i < k && len(items) > 0; i++ {
		best := 0
		bestScore := extractScore(items[0].Snippet)
		for j := 1; j < len(items); j++ {
			s := extractScore(items[j].Snippet)
			if s > bestScore {
				best = j
				bestScore = s
			}
		}
		res = append(res, items[best])
		items = append(items[:best], items[best+1:]...)
	}
	return res
}

func extractScore(snippet string) float64 {
	idx := strings.LastIndex(snippet, "(sim=")
	if idx == -1 {
		return -1
	}
	var s float64
	fmt.Sscanf(snippet[idx+5:], "%f)", &s)
	return s
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func buildPostgresDSN() string {
	host := os.Getenv("DB_HOST")
	dbName := os.Getenv("DB_NAME")
	user := os.Getenv("DB_USER")
	pass := os.Getenv("DB_PASS")

	if host == "" {
		log.Fatalf("DB_HOST not set for Postgres backend")
	}
	if dbName == "" {
		log.Fatalf("DB_NAME not set for Postgres backend")
	}
	if user == "" {
		log.Fatalf("DB_USER not set for Postgres backend")
	}

	dsn := fmt.Sprintf("user=%s password=%s dbname=%s host=%s", user, pass, dbName, host)
	if strings.HasPrefix(host, "/cloudsql/") {
		dsn += " sslmode=disable"
	}
	return dsn
}

// extractKialiSections builds per-section items for typical kiali.io docs.
// It extracts any heading h1/h2/h3 with an id as the section title,
// and aggregates subsequent <p> siblings until the next h1/h2/h3 heading.
func extractKialiSections(doc *goquery.Document, currURL string) []extractedSection {
	root := doc.Find(".td-content")
	if root.Length() == 0 {
		root = doc.Find("main")
	}
	if root.Length() == 0 {
		root = doc.Find("article")
	}
	var out []extractedSection

	headings := root.Find("h1[id],h2[id],h3[id]")
	if headings.Length() > 0 {
		headings.Each(func(_ int, h *goquery.Selection) {
			title := strings.TrimSpace(h.Text())
			id, _ := h.Attr("id")
			if title == "" {
				return
			}
			var b strings.Builder
			for sib := h.Next(); sib.Length() > 0; sib = sib.Next() {
				tag := goquery.NodeName(sib)
				if tag == "h1" || tag == "h2" || tag == "h3" {
					break
				}
				if tag == "p" {
					text := strings.TrimSpace(sib.Text())
					if text != "" {
						b.WriteString(text)
						b.WriteString("\n\n")
					}
				}
			}
			secURL := currURL
			if id != "" {
				secURL = secURL + "#" + id
			}
			out = append(out, extractedSection{Title: title, ID: id, Content: strings.TrimSpace(b.String()), URL: secURL})
		})
		return out
	}

	// Fallback: single section with page text
	title := strings.TrimSpace(doc.Find("title").Text())
	content := strings.TrimSpace(root.Text())
	out = append(out, extractedSection{Title: title, Content: content, URL: currURL})
	return out
}

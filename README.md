# Kiali AI MCP (Go RAG Service)

> ⚠️ Tech Preview: This is an experimental preview release. Functionality, APIs, data schemas, and behavior may change without notice. Not intended for production use.

RAG-backed chatbot service for Kiali/Istio. Ingests `kiali.io` docs and YouTube demos, stores embeddings (SQLite or Postgres+PGVector), and exposes a chat API secured via API key or Basic Auth.

### How it works
- Ingestion: crawl `kiali.io` docs; optionally expand YouTube playlists; chunk, embed, store vectors.
- Retrieval: embed query; nearest-neighbor over stored chunks.
- Generation: compose retrieved context + optional Kiali JSON, generate precise answer with citations.

### Models and providers
- Set `LLM_PROVIDER` to `gemini` or `openai` (default: `gemini`).
- Defaults:
  - Gemini: completion `gemini-1.5-flash`, embeddings `text-embedding-004`.
  - OpenAI: completion `gpt-4o-mini`, embeddings `text-embedding-3-small`.
- Override via `COMPLETION_MODEL` and `EMBEDDING_MODEL`. If you change embeddings, set `EMBEDDING_DIM` accordingly (e.g., 1536).

## Quickstart (local, SQLite)
```bash
# From repo root
export LLM_PROVIDER=gemini                # or: openai
export GEMINI_API_KEY=AIza...             # required if gemini
export OPENAI_API_KEY=sk-...              # required if openai
export VECTOR_BACKEND=sqlite
export VECTOR_DB_PATH=./data/rag.sqlite
export BASIC_AUTH_USER=kiali
export BASIC_AUTH_PASS=developer

# Option A: go run
go run ./cmd/server

# Option B: Makefile (uses env above)
make run
```

Health check:
```bash
curl -i http://localhost:8080/healthz | cat
```

## Configuration
You can configure via environment variables or a YAML file. Env vars take precedence. Copy `config.example.yaml` to `config.yaml` and set:

- **llm_provider**: `gemini` or `openai`
- **completion_model**, **embedding_model**: override defaults
- **gemini_api_key**, **openai_api_key**: set the one for your provider
- **vector_backend**: `sqlite` or `postgres`
- **vector_db_path**: SQLite path (when `sqlite`)
- **db_host, db_name, db_user, db_pass, embedding_dim**: Postgres settings (when `postgres`)
- **basic_auth_user, basic_auth_pass**: HTTP Basic credentials
- **server_addr**: default `:8080`
- **server_timeout_seconds**: default `60`

Use a config file:
```bash
cp config.example.yaml config.yaml
export CONFIG_FILE=$PWD/config.yaml
make run
```

## Auth
Send either:
- API key header: `X-API-Key: $API_KEY` (set `API_KEY` env on the server)
- or HTTP Basic: `Authorization: Basic base64(user:pass)` (use `BASIC_AUTH_USER`/`BASIC_AUTH_PASS`)

Example using Basic Auth:
```bash
AUTH="-u kiali:developer"  # adjust to your values
```

## API endpoints
Base URL: `http://localhost:8080`

- `GET /healthz` → `200 ok`
- `POST /v1/chat`
  - Request:
    ```json
    { "query": "How do I read the Kiali graph?", "context": { "kiali": { "graph": {} } } }
    ```
  - Response:
    ```json
    { "answer": "...", "citations": [{"title":"...","url":"...","span":"..."}], "used_models": {"completion_model":"...","embedding_model":"..."} }
    ```
- `POST /v1/ingest/kiali-docs`
  - Request: `{ "base_url": "https://kiali.io/docs/" }` (optional; defaults to `https://kiali.io/`)
  - Response: `{ "ingested": 5, "skipped": 2 }`
- `POST /v1/ingest/youtube`
  - Request: `{ "channel_or_playlist_url": "<yt playlist or comma-separated video URLs>" }`
  - Response: `{ "ingested": 3, "skipped": 1 }`
- `POST /v1/admin/clean` → `{ "removed_documents": 42 }`
- `POST /v1/admin/deduplicate` → `{ "removed_duplicates": 3 }`

## Common workflows

### 1) Ingest docs, then chat
```bash
# Ingest docs (defaults to kiali.io root if base_url omitted)
curl $AUTH -H 'Content-Type: application/json' \
  -d '{"base_url":"https://kiali.io/docs/"}' \
  http://localhost:8080/v1/ingest/kiali-docs | jq

# Ask a question
curl $AUTH -H 'Content-Type: application/json' \
  -d '{"query":"What does the traffic graph show?"}' \
  http://localhost:8080/v1/chat | jq
```

### 2) Ingest a YouTube playlist (or video list)
```bash
curl $AUTH -H 'Content-Type: application/json' \
  -d '{"channel_or_playlist_url":"https://www.youtube.com/playlist?list=PL..."}' \
  http://localhost:8080/v1/ingest/youtube | jq

# Or multiple URLs (comma-separated)
curl $AUTH -H 'Content-Type: application/json' \
  -d '{"channel_or_playlist_url":"https://youtu.be/ID1, https://youtu.be/ID2"}' \
  http://localhost:8080/v1/ingest/youtube | jq
```

### 3) Admin maintenance
```bash
# Remove all docs/embeddings
curl $AUTH -X POST http://localhost:8080/v1/admin/clean | jq

# Remove duplicate URLs
curl $AUTH -X POST http://localhost:8080/v1/admin/deduplicate | jq
```

## Run with Docker/Podman
Build and run locally:
```bash
# Build image (Podman example)
podman build -t quay.io/kiali/kiali-mcp:latest -f Dockerfile .

# Run (SQLite)
podman run --rm -p 8080:8080 \
  -e LLM_PROVIDER=gemini \
  -e GEMINI_API_KEY=AIza... \
  -e VECTOR_BACKEND=sqlite \
  -e VECTOR_DB_PATH=/data/rag.sqlite \
  -e BASIC_AUTH_USER=kiali -e BASIC_AUTH_PASS=developer \
  -v $PWD/data:/data \
  quay.io/kiali/kiali-mcp:latest
```

## Makefile targets
```bash
# Build all
make build

# Run locally with env vars
env GEMINI_API_KEY=... make run

# Build/push container (Podman)
make container-build IMAGE=quay.io/you/kiali-mcp:dev
make container-push IMAGE=quay.io/you/kiali-mcp:dev

# Deploy to OpenShift via template
make openshift-deploy IMAGE=quay.io/you/kiali-mcp:dev NAMESPACE=istio-system BACKEND=sqlite VECTOR_DB_PATH=/data/rag.sqlite
```

## OpenShift deployment (namespace `istio-system`)
Create the project if needed:
```bash
oc get ns istio-system || oc new-project istio-system
```

Deploy using the provided template:
```bash
# SQLite + PVC example
oc -n istio-system process -f deploy/openshift-template.yaml \
  -p NAME=kiali-ai-mcp \
  -p IMAGE=REGISTRY/PROJECT/kiali-ai-mcp:latest \
  -p VECTOR_BACKEND=sqlite \
  -p VECTOR_DB_PATH=/data/rag.sqlite \
  -p LLM_PROVIDER=gemini \
  -p GEMINI_API_KEY=AIza... \
  | oc -n istio-system apply -f -

# Postgres example
oc -n istio-system process -f deploy/openshift-template.yaml \
  -p NAME=kiali-ai-mcp \
  -p IMAGE=REGISTRY/PROJECT/kiali-ai-mcp:latest \
  -p VECTOR_BACKEND=postgres \
  -p DB_HOST=my-postgres:5432 \
  -p DB_NAME=kiali_ai \
  -p DB_USER=kiali_ai \
  -p DB_PASS=StrongPass! \
  -p EMBEDDING_DIM=1536 \
  -p LLM_PROVIDER=openai \
  -p OPENAI_API_KEY=sk-... \
  | oc -n istio-system apply -f -

# Get route
oc -n istio-system get route kiali-ai-mcp -o jsonpath='{.spec.host}{"\n"}'

# Let access to Kiali
oc create sa kiali-mcp -n istio-system
oc adm policy add-cluster-role-to-user view -z kiali-mcp -n istio-system
oc adm policy add-cluster-role-to-user cluster-reader -z kiali-mcp
```

## Google Cloud Run + Cloud SQL (Postgres + PGVector)
```bash
PROJECT_ID=... ; REGION=us-central1 ; INSTANCE=kiali-ai-pg ; DBNAME=kiali_ai ; DBUSER=kiali_ai ; DBPASS='StrongPass!'

# Create Cloud SQL instance + db/user
gcloud sql instances create $INSTANCE --project=$PROJECT_ID --database-version=POSTGRES_15 --cpu=2 --memory=4GiB --region=$REGION
gcloud sql databases create $DBNAME --instance=$INSTANCE --project=$PROJECT_ID
gcloud sql users create $DBUSER --instance=$INSTANCE --project=$PROJECT_ID --password=$DBPASS

# Build & push container
AR_REPO=kiali-ai
SERVICE=kiali-ai-mcp
IMAGE=$REGION-docker.pkg.dev/$PROJECT_ID/$AR_REPO/$SERVICE:latest
SQL_CONN_NAME="$PROJECT_ID:$REGION:$INSTANCE"

gcloud artifacts repositories create $AR_REPO --repository-format=docker --location=$REGION || true
gcloud builds submit --tag $IMAGE .

# Deploy (Gemini)
gcloud run deploy $SERVICE \
  --image $IMAGE \
  --region $REGION --platform managed --allow-unauthenticated --port 8080 \
  --add-cloudsql-instances $SQL_CONN_NAME \
  --set-env-vars VECTOR_BACKEND=postgres,DB_HOST=/cloudsql/$SQL_CONN_NAME,DB_NAME=$DBNAME,DB_USER=$DBUSER,DB_PASS=$DBPASS \
  --set-env-vars LLM_PROVIDER=gemini,GEMINI_API_KEY=AIza...,COMPLETION_MODEL=gemini-1.5-flash,EMBEDDING_MODEL=text-embedding-004,EMBEDDING_DIM=1536

# Deploy (OpenAI)
gcloud run deploy $SERVICE \
  --image $IMAGE \
  --region $REGION --platform managed --allow-unauthenticated --port 8080 \
  --add-cloudsql-instances $SQL_CONN_NAME \
  --set-env-vars VECTOR_BACKEND=postgres,DB_HOST=/cloudsql/$SQL_CONN_NAME,DB_NAME=$DBNAME,DB_USER=$DBUSER,DB_PASS=$DBPASS \
  --set-env-vars LLM_PROVIDER=openai,OPENAI_API_KEY=sk-...,COMPLETION_MODEL=gpt-4o-mini,EMBEDDING_MODEL=text-embedding-3-small,EMBEDDING_DIM=1536
```
The service initializes `CREATE EXTENSION IF NOT EXISTS vector;` and required tables on first run.

## Scripts
- `scripts/deploy_cloud_run_pgvector.sh`: end-to-end build and deploy to Cloud Run + Cloud SQL.
- `scripts/seed_ingestion.sh`: basic seeding of docs/YouTube URLs.
- `scripts/redeploy_image.sh`: convenience redeploy.
- `scripts/remove_all.sh`: cleanup.

## Model-specific env examples

### Using Gemini
```bash
export LLM_PROVIDER=gemini
export GEMINI_API_KEY=AIza...
export COMPLETION_MODEL=gemini-1.5-flash
export EMBEDDING_MODEL=text-embedding-004
make run
```

### Using OpenAI
```bash
export LLM_PROVIDER=openai
export OPENAI_API_KEY=sk-...
export COMPLETION_MODEL=gpt-4o-mini
export EMBEDDING_MODEL=text-embedding-3-small
# If you choose a different embedding model, set EMBEDDING_DIM accordingly (e.g., 1536)
make run
``` 
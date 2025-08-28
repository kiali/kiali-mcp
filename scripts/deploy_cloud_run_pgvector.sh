#!/usr/bin/env bash
set -euo pipefail

# Configuration (export beforehand or edit here)
: "${PROJECT_ID:?Set PROJECT_ID}"
: "${REGION:=europe-west4}"
: "${INSTANCE:=kiali-ai-pg}"
: "${DBNAME:=kiali_ai}"
: "${DBUSER:=kiali_ai}"
: "${DBPASS:?Set DBPASS (temporary; prefer Secret Manager in prod)}"
: "${AR_REPO:=kiali-ai}"
: "${SERVICE:=kiali-ai-mcp}"
: "${GEMINI_API_KEY:?Set GEMINI_API_KEY}" # or use Secret Manager
: "${EMBEDDING_DIM:=768}" # Gemini text-embedding-004 returns 768-dim


#GEMINI KEY

SQL_CONN_NAME="$PROJECT_ID:$REGION:$INSTANCE"
IMAGE="$REGION-docker.pkg.dev/$PROJECT_ID/$AR_REPO/$SERVICE:latest"

# Enable APIs
gcloud services enable run.googleapis.com artifactregistry.googleapis.com cloudbuild.googleapis.com sqladmin.googleapis.com --project "$PROJECT_ID"

# Create Artifact Registry
gcloud artifacts repositories create "$AR_REPO" --repository-format=docker --location="$REGION" --project "$PROJECT_ID" || true

# Create Cloud SQL instance, db, user (idempotent-ish)
gcloud sql instances create "$INSTANCE" --project="$PROJECT_ID" --database-version=POSTGRES_15 --cpu=2 --memory=4GiB --region="$REGION" || true
sleep 5
(gcloud sql databases create "$DBNAME" --instance="$INSTANCE" --project="$PROJECT_ID" || true)
(gcloud sql users create "$DBUSER" --instance="$INSTANCE" --project="$PROJECT_ID" --password="$DBPASS" || true)

# Build & push
SOURCE_DIR="$(cd "$(dirname "$0")/.." && pwd)"
gcloud builds submit --tag "$IMAGE" "$SOURCE_DIR" --project "$PROJECT_ID" --machine-type=e2-medium

# Deploy to Cloud Run
gcloud run deploy "$SERVICE" \
  --image "$IMAGE" \
  --region "$REGION" --platform managed --allow-unauthenticated --port 8080 \
  --add-cloudsql-instances "$SQL_CONN_NAME" \
  --set-env-vars VECTOR_BACKEND=postgres,DB_HOST=/cloudsql/$SQL_CONN_NAME,DB_NAME=$DBNAME,DB_USER=$DBUSER,DB_PASS=$DBPASS \
  --set-env-vars GEMINI_API_KEY=$GEMINI_API_KEY,COMPLETION_MODEL=gemini-1.5-flash,EMBEDDING_MODEL=text-embedding-004,EMBEDDING_DIM=$EMBEDDING_DIM \
  --project "$PROJECT_ID"

echo "Deployed service: $(gcloud run services describe "$SERVICE" --region "$REGION" --format='value(status.url)' --project "$PROJECT_ID")" 



#!/usr/bin/env bash
set -euo pipefail

# Rebuild and redeploy Cloud Run service image (no infra changes)
# Usage:
#   PROJECT_ID=your-project REGION=europe-west4 ./scripts/redeploy_image.sh
# Optional envs:
#   AR_REPO=kiali-ai SERVICE=kiali-ai-mcp IMAGE_TAG=latest GCLOUD_MACHINE_TYPE=e2-medium

: "${PROJECT_ID:?Set PROJECT_ID}"
: "${REGION:=europe-west4}"
: "${AR_REPO:=kiali-ai}"
: "${SERVICE:=kiali-ai-mcp}"
: "${IMAGE_TAG:=latest}"
: "${GCLOUD_MACHINE_TYPE:=e2-medium}"

SOURCE_DIR="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE="$REGION-docker.pkg.dev/$PROJECT_ID/$AR_REPO/$SERVICE:$IMAGE_TAG"

# Ensure APIs and repo (idempotent)
gcloud services enable run.googleapis.com artifactregistry.googleapis.com cloudbuild.googleapis.com --project "$PROJECT_ID" >/dev/null 2>&1 || true
gcloud artifacts repositories create "$AR_REPO" --repository-format=docker --location="$REGION" --project "$PROJECT_ID" >/dev/null 2>&1 || true

# Build & push
echo "Building and pushing image: $IMAGE"
gcloud builds submit --tag "$IMAGE" "$SOURCE_DIR" --project "$PROJECT_ID" --machine-type="$GCLOUD_MACHINE_TYPE"

# Deploy updating only the image; preserves env vars, Cloud SQL, IAM, etc.
echo "Deploying service: $SERVICE"
gcloud run deploy "$SERVICE" \
  --image "$IMAGE" \
  --region "$REGION" \
  --platform managed \
  --project "$PROJECT_ID"

URL=$(gcloud run services describe "$SERVICE" --region "$REGION" --format='value(status.url)' --project "$PROJECT_ID")
echo "Deployed revision URL: $URL" 
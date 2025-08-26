#!/usr/bin/env bash
# destroy_gcp.sh - Tear down Kiali AI MCP infra on GCP (Cloud Run, Cloud SQL, Artifact Registry, optional Secrets)
set -euo pipefail

# Required env
: "${PROJECT_ID:?Set PROJECT_ID}"
: "${REGION:=europe-west4}"
: "${SERVICE:=kiali-ai-mcp}"
: "${AR_REPO:=kiali-ai}"                # Artifact Registry repo name
: "${INSTANCE:=kiali-ai-pg}"            # Cloud SQL instance
: "${DBNAME:=kiali_ai}"                 # Database name
: "${DBUSER:=kiali_ai}"                 # Database user
: "${SECRET_NAMES:=}"                   # Optional: space-separated secret names to delete (e.g., "GEMINI_API_KEY API_KEY")

gcloud config set project "$PROJECT_ID" >/dev/null

echo "Deleting Cloud Run service: $SERVICE"
gcloud run services delete "$SERVICE" --region "$REGION" --platform managed --quiet || true

echo "Deleting Cloud SQL database and user (if exist)"
gcloud sql databases delete "$DBNAME" --instance "$INSTANCE" --quiet || true
gcloud sql users delete "$DBUSER" --instance "$INSTANCE" --quiet || true

echo "Deleting Cloud SQL instance: $INSTANCE"
gcloud sql instances delete "$INSTANCE" --quiet || true

echo "Deleting Artifact Registry contents in repo: $AR_REPO ($REGION)"
# Delete all versions in all packages
PKGS=$(gcloud artifacts packages list --repository="$AR_REPO" --location="$REGION" --format='value(name)' || true)
for PKG in $PKGS; do
  VERS=$(gcloud artifacts versions list --package="$PKG" --repository="$AR_REPO" --location="$REGION" --format='value(name)' || true)
  for VER in $VERS; do
    gcloud artifacts versions delete "$VER" --package="$PKG" --repository="$AR_REPO" --location="$REGION" --quiet || true
  done
  gcloud artifacts packages delete "$PKG" --repository="$AR_REPO" --location="$REGION" --quiet || true
done

echo "Deleting Artifact Registry repo: $AR_REPO"
gcloud artifacts repositories delete "$AR_REPO" --location="$REGION" --quiet || true

if [[ -n "${SECRET_NAMES}" ]]; then
  echo "Deleting secrets: $SECRET_NAMES"
  for S in $SECRET_NAMES; do
    gcloud secrets delete "$S" --quiet || true
  done
fi

echo "Done."
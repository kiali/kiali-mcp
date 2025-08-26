SHELL := /usr/bin/bash

# Image configuration
IMAGE_REGISTRY ?= quay.io
IMAGE_REPO ?= kiali/kiali-mcp
IMAGE_TAG ?= latest
IMAGE ?= $(IMAGE_REGISTRY)/$(IMAGE_REPO):$(IMAGE_TAG)

# App configuration
NAME ?= kiali-ai-mcp
NAMESPACE ?= istio-system
BACKEND ?= sqlite                  # sqlite|postgres
VECTOR_DB_PATH ?= ./data/rag.sqlite
EMBEDDING_DIM ?= 1536
LLM_PROVIDER ?= gemini             # gemini|openai
GEMINI_API_KEY ?=
OPENAI_API_KEY ?=
COMPLETION_MODEL ?= gemini-1.5-flash
EMBEDDING_MODEL ?= text-embedding-004
BASIC_AUTH_USER ?= kiali
BASIC_AUTH_PASS ?= developer
SERVER_ADDR ?= :8080
ROUTE_HOST ?=
CONFIG_FILE ?=

# Kiali graph tool configuration
KIALI_API_BASE ?=
KIALI_BEARER_TOKEN ?=
KIALI_TLS_INSECURE ?= false
KIALI_CA_FILE ?=

# Paths
DEPLOY_TEMPLATE := deploy/openshift-template.yaml

.PHONY: all build tidy run docker-build docker-push container push openshift-deploy openshift-delete

all: build

build:
	go build ./...

tidy:
	go mod tidy

run:
	LLM_PROVIDER=$(LLM_PROVIDER) \
	GEMINI_API_KEY=$(GEMINI_API_KEY) \
	OPENAI_API_KEY=$(OPENAI_API_KEY) \
	COMPLETION_MODEL=$(COMPLETION_MODEL) \
	EMBEDDING_MODEL=$(EMBEDDING_MODEL) \
	VECTOR_BACKEND=$(BACKEND) \
	VECTOR_DB_PATH=$(VECTOR_DB_PATH) \
	EMBEDDING_DIM=$(EMBEDDING_DIM) \
	BASIC_AUTH_USER=$(BASIC_AUTH_USER) \
	BASIC_AUTH_PASS=$(BASIC_AUTH_PASS) \
	SERVER_ADDR=$(SERVER_ADDR) \
	CONFIG_FILE=$(CONFIG_FILE) \
	KIALI_API_BASE=$(KIALI_API_BASE) \
	KIALI_BEARER_TOKEN=$(KIALI_BEARER_TOKEN) \
	KIALI_TLS_INSECURE=$(KIALI_TLS_INSECURE) \
	KIALI_CA_FILE=$(KIALI_CA_FILE) \
	go run ./cmd/server

# Container
container: container-build

container-build:
	podman build \
	  --build-arg VCS_REF=$$(git rev-parse --short=12 HEAD 2>/dev/null || echo unknown) \
	  --build-arg BUILD_DATE=$$(date -u +"%Y-%m-%dT%H:%M:%SZ") \
	  -t $(IMAGE) -f Dockerfile .

container-push:
	podman push $(IMAGE)

push: container-push

# OpenShift
openshift-deploy:
	@[ -f $(DEPLOY_TEMPLATE) ] || { echo "Missing $(DEPLOY_TEMPLATE)"; exit 1; }
	oc get ns $(NAMESPACE) >/dev/null 2>&1 || oc new-project $(NAMESPACE)
	oc -n $(NAMESPACE) process -f $(DEPLOY_TEMPLATE) \
		-p NAME=$(NAME) \
		-p IMAGE=$(IMAGE) \
		-p REPLICAS=1 \
		-p VECTOR_BACKEND=$(BACKEND) \
		-p VECTOR_DB_PATH=$(VECTOR_DB_PATH) \
		-p LLM_PROVIDER=$(LLM_PROVIDER) \
		-p GEMINI_API_KEY=$(GEMINI_API_KEY) \
		-p OPENAI_API_KEY=$(OPENAI_API_KEY) \
		-p COMPLETION_MODEL=$(COMPLETION_MODEL) \
		-p EMBEDDING_MODEL=$(EMBEDDING_MODEL) \
		-p EMBEDDING_DIM=$(EMBEDDING_DIM) \
		-p BASIC_AUTH_USER=$(BASIC_AUTH_USER) \
		-p BASIC_AUTH_PASS=$(BASIC_AUTH_PASS) \
		-p DB_HOST=$(DB_HOST) \
		-p DB_NAME=$(DB_NAME) \
		-p DB_USER=$(DB_USER) \
		-p DB_PASS=$(DB_PASS) \
		-p SERVER_ADDR=$(SERVER_ADDR) \
		-p ROUTE_HOST=$(ROUTE_HOST) \
		-p KIALI_API_BASE=$(KIALI_API_BASE) \
		-p KIALI_BEARER_TOKEN=$(KIALI_BEARER_TOKEN) \
		-p KIALI_TLS_INSECURE=$(KIALI_TLS_INSECURE) \
		-p KIALI_CA_FILE=$(KIALI_CA_FILE) \
		| oc -n $(NAMESPACE) apply -f -
	@echo -n "Route: https://" ; oc -n $(NAMESPACE) get route $(NAME) -o jsonpath='{.spec.host}{"\n"}'

openshift-delete:
	-oc -n $(NAMESPACE) delete route/$(NAME) svc/$(NAME) deploy/$(NAME) >/dev/null 2>&1 || true
	-oc -n $(NAMESPACE) delete pvc/$(NAME)-data >/dev/null 2>&1 || true

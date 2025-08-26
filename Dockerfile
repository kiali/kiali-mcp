# syntax=docker/dockerfile:1.6
FROM golang:1.23 AS build
WORKDIR /src
# Leverage module cache
COPY go.* ./
RUN go mod download
# Copy rest of source
COPY . .
# Build (CGO-free)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/server ./cmd/server

FROM gcr.io/distroless/base-debian12:nonroot
WORKDIR /
COPY --from=build /out/server /server
# Use numeric UID/GID for OpenShift compatibility
USER 65532:65532
ENV SERVER_ADDR=:8080
# Default basic auth for convenience (override in deployment)
ENV BASIC_AUTH_USER=kiali
ENV BASIC_AUTH_PASS=developer
# Gemini provider (set GEMINI_API_KEY at deploy time)
# ENV GEMINI_API_KEY=
# ENV COMPLETION_MODEL=gemini-1.5-flash
# ENV EMBEDDING_MODEL=text-embedding-004
EXPOSE 8080
ENTRYPOINT ["/server"] 
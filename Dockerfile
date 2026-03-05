# Stage 1: Build
# CGO is required for mattn/go-sqlite3.
FROM --platform=linux/arm64 golang:1.24-alpine AS builder

RUN apk add --no-cache gcc musl-dev sqlite-dev

WORKDIR /src

# Download dependencies first (cached layer unless go.mod/go.sum change)
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 GOOS=linux GOARCH=arm64 \
    go build -ldflags="-s -w" -o /cgm-get-agent ./cmd/server

# Stage 2: Runtime
FROM --platform=linux/arm64 alpine:3.19

# ca-certificates: HTTPS to Dexcom API
# sqlite-libs:     runtime SQLite shared library for CGO binary
# wget:            Docker healthcheck (GET /health)
RUN apk add --no-cache ca-certificates sqlite-libs wget

COPY --from=builder /cgm-get-agent /usr/local/bin/cgm-get-agent

EXPOSE 8080

ENTRYPOINT ["cgm-get-agent", "serve"]

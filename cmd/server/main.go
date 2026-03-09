package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/johnmartinez/cgm-get-agent/internal/config"
	"github.com/johnmartinez/cgm-get-agent/internal/dexcom"
	"github.com/johnmartinez/cgm-get-agent/internal/mcp"
	"github.com/johnmartinez/cgm-get-agent/internal/rest"
	"github.com/johnmartinez/cgm-get-agent/internal/store"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: cgm-get-agent serve")
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError})).Error("config load failed", "error", err)
		os.Exit(1)
	}

	var slogLevel slog.Level
	switch cfg.LogLevel {
	case 1:
		slogLevel = slog.LevelError
	case 3:
		slogLevel = slog.LevelDebug
	default:
		slogLevel = slog.LevelInfo
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slogLevel}))
	slog.SetDefault(logger)

	st, err := store.Open(cfg.Storage.DBPath)
	if err != nil {
		logger.Error("store open failed", "error", err)
		os.Exit(1)
	}
	defer st.Close()

	oauth := dexcom.NewOAuthHandler(cfg)
	client := dexcom.NewClient(cfg, oauth)
	mcpServer := mcp.New(cfg, st, oauth, client)

	transport := os.Getenv("GA_MCP_TRANSPORT")
	if transport == "" {
		transport = "sse"
	}

	if transport == "stdio" {
		logger.Info("starting MCP server", "transport", "stdio")
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		if err := mcpServer.RunStdio(ctx); err != nil && err != context.Canceled {
			logger.Error("stdio server error", "error", err)
			os.Exit(1)
		}
		return
	}

	// SSE mode: HTTP server with MCP, OAuth, health, and tool-invoke endpoints.
	restHandler := rest.New(oauth, st, mcpServer.StartTime(), mcpServer)

	mux := http.NewServeMux()
	mux.Handle("/sse", mcpServer.SSEHandler())
	mux.HandleFunc("/oauth/start", oauth.HandleStart)
	mux.HandleFunc("/callback", oauth.HandleCallback)
	mux.HandleFunc("/health", restHandler.HandleHealth)
	mux.HandleFunc("/v1/tools/invoke", restHandler.HandleToolInvoke)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	logger.Info("starting MCP server", "transport", "sse", "addr", addr)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		<-ctx.Done()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutCancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			logger.Error("shutdown error", "error", err)
		}
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("HTTP server error", "error", err)
		os.Exit(1)
	}
}

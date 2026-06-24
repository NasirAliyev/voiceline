// Command server is the Voiceline composition root: it loads config, wires the
// adapters behind their domain ports, starts the HTTP server and worker pool,
// and shuts down gracefully. It is the only place permitted to call os.Exit.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nasiraliev/voiceline/internal/adapter/logdest"
	"github.com/nasiraliev/voiceline/internal/adapter/memstore"
	"github.com/nasiraliev/voiceline/internal/adapter/openai"
	"github.com/nasiraliev/voiceline/internal/adapter/sheets"
	"github.com/nasiraliev/voiceline/internal/app"
	"github.com/nasiraliev/voiceline/internal/config"
	"github.com/nasiraliev/voiceline/internal/domain"
	"github.com/nasiraliev/voiceline/internal/transport/httpapi"
)

const (
	healthcheckCommand = "healthcheck"
	healthcheckTimeout = 3 * time.Second
	defaultHealthPort  = "8080"
)

func main() {
	// Subcommand used by the container HEALTHCHECK (distroless has no shell).
	if len(os.Args) > 1 && os.Args[1] == healthcheckCommand {
		if err := healthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, "healthcheck failed:", err)
			os.Exit(1)
		}
		return
	}

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := newLogger(cfg.Log)
	slog.SetDefault(logger)

	// Root context is cancelled on SIGINT/SIGTERM, triggering graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Wire adapters behind their domain ports. Swapping the LLM provider or the
	// destination is purely a change here — the pipeline below is untouched.
	llmClient := openai.NewClient(cfg.OpenAI.BaseURL, cfg.OpenAI.APIKey, cfg.OpenAI.RequestTimeout, cfg.OpenAI.MaxRetries, logger)
	transcriber := openai.NewTranscriber(llmClient, cfg.OpenAI.TranscriptionModel)
	analyzer := openai.NewAnalyzer(llmClient, cfg.OpenAI.AnalysisModel)

	destination, err := newDestination(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("init destination: %w", err)
	}

	store := memstore.New()
	processor := app.NewProcessor(transcriber, analyzer, destination, store, time.Now)
	pool := app.NewPool(processor, cfg.Pipeline.Workers, cfg.Pipeline.QueueSize, cfg.Pipeline.ProcessingTimeout, logger)
	pool.Start()

	handlers := httpapi.NewHandlers(store, pool, logger, cfg.Upload.MaxAudioBytes, cfg.Upload.AllowedAudioTypes, "")
	router := httpapi.NewRouter(handlers, logger, cfg.Auth.APIKey)

	server := &http.Server{
		Addr:              ":" + cfg.Server.Port,
		Handler:           router,
		ReadHeaderTimeout: cfg.Server.ReadTimeout, // slowloris protection
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("server listening",
			slog.String("addr", server.Addr),
			slog.String("destination", cfg.Sink.Destination),
			slog.Int("workers", cfg.Pipeline.Workers),
			slog.Bool("auth_enabled", cfg.AuthEnabled()),
		)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("server error: %w", err)
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining in-flight work")
	}

	return shutdown(server, pool, cfg.Server.ShutdownTimeout, logger)
}

// newDestination selects the delivery adapter from config.
func newDestination(ctx context.Context, cfg config.Config, logger *slog.Logger) (domain.Destination, error) {
	switch cfg.Sink.Destination {
	case config.DestinationSheets:
		return sheets.New(ctx, cfg.Sink.SpreadsheetID, cfg.Sink.SheetName, cfg.Sink.CredentialsFile)
	case config.DestinationLog:
		return logdest.New(logger), nil
	default:
		return nil, fmt.Errorf("unknown destination %q", cfg.Sink.Destination)
	}
}

// shutdown stops accepting new requests, then drains in-flight jobs within the
// configured timeout.
func shutdown(server *http.Server, pool *app.Pool, timeout time.Duration, logger *slog.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("http server shutdown error", slog.Any("error", err))
	}
	if err := pool.Shutdown(ctx); err != nil {
		return fmt.Errorf("worker pool drain incomplete: %w", err)
	}
	logger.Info("shutdown complete")
	return nil
}

// healthcheck performs the container liveness probe against the local server.
func healthcheck() error {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultHealthPort
	}
	ctx, cancel := context.WithTimeout(context.Background(), healthcheckTimeout)
	defer cancel()

	url := fmt.Sprintf("http://127.0.0.1:%s%s", port, httpapi.RouteHealth)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: healthcheckTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

func newLogger(cfg config.LogConfig) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.Level)}
	var handler slog.Handler
	if cfg.Format == config.LogFormatText {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

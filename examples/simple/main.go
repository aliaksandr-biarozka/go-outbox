package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/aliaksandr-biarozka/go-outbox"
)

type exampleSource struct{}

func (s *exampleSource) GetItems(ctx context.Context, batchSize int) ([]outbox.Item, error) {
	return nil, nil
}

func (s *exampleSource) MarkAsSent(ctx context.Context, item outbox.Item) error {
	return nil
}

type exampleDestination struct{}

func (d *exampleDestination) Send(ctx context.Context, item outbox.Item) error {
	return nil
}

func (d *exampleDestination) SendMany(ctx context.Context, items []outbox.Item) error {
	for _, item := range items {
		if err := d.Send(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource: true,
		Level:     slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	defer func() {
		if p := recover(); p != nil {
			logger.Error("panic caught", "panic", p)
			os.Exit(1)
		}
	}()

	source := &exampleSource{}
	destination := &exampleDestination{}

	ob := outbox.New(source, destination, outbox.Config{
		BatchSize:           30,
		MaxTries:            3,
		MaxSleepSec:         5,
		MaxConcurrentGroups: 30,
	}, logger)

	ctx, cancel := context.WithCancel(context.Background())

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		logger.Info("signal received, shutting down gracefully", slog.Any("signal", sig))
		cancel()
	}()

	if err := ob.Run(ctx); err != context.Canceled {
		logger.Error("failed to run outbox", "error", err)
		os.Exit(1)
	}

	logger.Info("service stopped")
}

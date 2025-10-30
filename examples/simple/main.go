package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/aliaksandr-biarozka/go-outbox"
)

type exampleItem struct {
	entityId string
	id       string
	sequence int64
}

func (e exampleItem) GetEntityId() string { return e.entityId }
func (e exampleItem) GetId() string       { return e.id }
func (e exampleItem) GetSequence() int64  { return e.sequence }

type exampleSource struct {
	items []exampleItem
}

func (s *exampleSource) GetItems(ctx context.Context, batchSize int) ([]exampleItem, error) {
	return s.items, nil
}

func (s *exampleSource) Acknowledge(ctx context.Context, item exampleItem) error {
	s.items = slices.DeleteFunc(s.items, func(i exampleItem) bool {
		return i.id == item.id
	})
	return nil
}

type exampleDestination struct{}

func (d *exampleDestination) Send(ctx context.Context, item exampleItem) error {
	return nil
}

func (d *exampleDestination) SendMany(ctx context.Context, items []exampleItem) error {
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

	source := &exampleSource{
		items: []exampleItem{
			{entityId: "entity1", id: "1", sequence: 1},
			{entityId: "entity1", id: "2", sequence: 2},
			{entityId: "entity1", id: "3", sequence: 3},
			{entityId: "entity2", id: "4", sequence: 4},
			{entityId: "entity2", id: "5", sequence: 5},
			{entityId: "entity2", id: "6", sequence: 6},
		},
	}
	destination := &exampleDestination{}

	ob := outbox.New[exampleItem](source, destination, outbox.Config{
		BatchSize:           30,
		SleepSec:            5,
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

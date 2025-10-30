package outbox

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/google/uuid"
)

type Source interface {
	GetItems(ctx context.Context, batchSize int) ([]Item, error)
	MarkAsSent(ctx context.Context, item Item) error
}

type Item interface {
	// Context specific identifier, e.g. userId, accountId and etc.
	GetEntityId() string
	// Unique identifier of the item, e.g. messageId, orderId and etc.
	// Usually there are many items for the same entity.
	GetId() string
}

type Destination interface {
	SendMany(ctx context.Context, items []Item) error
	Send(ctx context.Context, items Item) error
}

type Config struct {
	BatchSize           int
	MaxTries            int
	MaxSleepSec         int
	MaxConcurrentGroups int
}

type Outbox struct {
	logger      *slog.Logger
	source      Source
	destination Destination
	config      Config
	semaphore   chan struct{}
}

func New(source Source, destination Destination, config Config, logger *slog.Logger) *Outbox {
	if config.BatchSize <= 0 {
		config.BatchSize = 30
	}
	if config.MaxTries <= 0 {
		config.MaxTries = 3
	}
	if config.MaxSleepSec <= 0 {
		config.MaxSleepSec = 5
	}
	if config.MaxConcurrentGroups <= 0 {
		config.MaxConcurrentGroups = 30
	}

	return &Outbox{
		logger:      logger,
		source:      source,
		destination: destination,
		config:      config,
		semaphore:   make(chan struct{}, config.MaxConcurrentGroups),
	}
}

func (o *Outbox) Run(ctx context.Context) error {
	o.logger.InfoContext(ctx, "starting outbox with config", slog.Group("config",
		slog.Int("batch_size", o.config.BatchSize),
		slog.Int("max_tries", o.config.MaxTries),
		slog.Int("max_sleep_sec", o.config.MaxSleepSec),
	))

	defer func() {
		o.logger.InfoContext(ctx, "outbox stopped")
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			items, err := o.source.GetItems(ctx, o.config.BatchSize)
			if err != nil {
				o.logger.ErrorContext(ctx, "failed to get items", slog.Any("error", err))
				continue
			}

			if len(items) == 0 {
				o.logger.DebugContext(ctx, "no items to process. sleeping for 5 seconds")
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
					<-time.After(time.Duration(o.config.MaxSleepSec) * time.Second)
					continue
				}
			}

			groupedItems := make(map[string][]Item)
			for _, item := range items {
				groupedItems[item.GetEntityId()] = append(groupedItems[item.GetEntityId()], item)
			}

			logger := o.logger.With("run_id", uuid.New().String())
			logger.DebugContext(ctx, "processing items", slog.Int("count", len(items)))
			var wg sync.WaitGroup
			for groupName, items := range groupedItems {
				groupLogger := logger.With("entity_id", groupName)
				wg.Add(1)
				go func(items []Item, l *slog.Logger) {
					defer wg.Done()

					select {
					case o.semaphore <- struct{}{}:
						defer func() { <-o.semaphore }()
					case <-ctx.Done():
						return
					}

					for _, item := range items {
						_, err := backoff.Retry(ctx, func() (bool, error) {
							if err := o.destination.Send(ctx, item); err != nil {
								l.ErrorContext(ctx, "failure sending item to destination. retrying...", slog.Any("error", err))
								return false, err
							}
							return true, nil
						}, backoff.WithMaxTries(uint(o.config.MaxTries)))

						if err != nil {
							l.ErrorContext(ctx, "failed to send item", slog.Any("error", err), slog.String("item_id", item.GetId()))
							return
						}

						_, err = backoff.Retry(ctx, func() (bool, error) {
							if err := o.source.MarkAsSent(ctx, item); err != nil {
								l.ErrorContext(ctx, "failure marking item as sent in source. retrying...", slog.Any("error", err))
								return false, err
							}
							return true, nil
						}, backoff.WithMaxTries(uint(o.config.MaxTries)))

						if err != nil {
							l.ErrorContext(ctx, "failed to mark as sent", slog.Any("error", err), slog.String("item_id", item.GetId()))
							return
						}
					}
				}(items, groupLogger)
			}
			wg.Wait()
			logger.DebugContext(ctx, "items were processed successfully")
		}
	}
}

// Package outbox provides a robust implementation of the transactional outbox pattern
// for reliable message delivery between a database and a message broker.
//
// The outbox pattern ensures at-least-once delivery semantics by:
//   - Fetching unsent items from a persistent source (database)
//   - Grouping items by entity ID for ordered processing
//   - Sending items to a destination (message queue) with automatic retries
//   - Marking items as sent after successful delivery
//
// Example usage:
//
//	source := &MyDatabaseSource{}
//	destination := &MyMessageQueueDestination{}
//	config := outbox.Config{
//	    BatchSize:           30,
//	    SleepSec:            5,
//	    MaxConcurrentGroups: 30,
//	}
//	ob := outbox.New(source, destination, config, logger)
//	if err := ob.Run(ctx); err != nil {
//	    log.Fatal(err)
//	}
package outbox

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Source represents a persistent storage (typically a database) that provides items
// to be processed by the outbox. Items are fetched in batches and removed
// after successful delivery to the destination.
//
// Implementation notes:
//   - GetItems should return items ordered by creation time for predictable processing
//   - Acknowledge should either DELETE the item (recommended) or mark it as sent with UPDATE
//   - Both methods should handle context cancellation gracefully
type Source[T Item] interface {
	// GetItems retrieves a batch of unprocessed items from the source.
	// It should return an empty slice when no items are available.
	GetItems(ctx context.Context, batchSize int) ([]T, error)

	// Acknowledge confirms that an item was successfully sent to the destination.
	// Typical implementations:
	//   - DELETE FROM outbox WHERE id = ? (recommended for performance)
	//   - UPDATE outbox SET sent_at = NOW() WHERE id = ? (for audit trails)
	Acknowledge(ctx context.Context, item T) error
}

// Item represents a single unit of work in the outbox pattern.
// Each item belongs to an entity (for grouping) and has a unique identifier.
//
// Items with the same EntityId are processed sequentially to maintain ordering,
// while items with different EntityIds can be processed concurrently.
type Item interface {
	// GetEntityId returns a context-specific identifier used for grouping items.
	// Examples: userId, accountId, tenantId, orderId
	// Items with the same entity ID are processed sequentially to maintain order.
	GetEntityId() string

	// GetId returns a unique identifier for this specific item.
	// Examples: messageId, eventId, transactionId
	// This is used for logging and tracking individual item processing.
	GetId() string

	// GetSequence returns a monotonically increasing value used for ordering items.
	// Items are processed in ascending sequence order within each entity group.
	// Common implementations:
	//   - Unix timestamp in milliseconds: time.Now().UnixMilli()
	//   - Database auto-increment ID
	//   - Custom version/sequence number
	// Items with the same sequence are processed in arbitrary order.
	GetSequence() int64
}

// Destination represents a target system (typically a message queue or API) where
// items are sent after being retrieved from the source.
//
// Implementation notes:
//   - Send should be idempotent when possible (same item sent twice should not cause issues)
//   - SendMany can be optimized for batch sending if the destination supports it
//   - Both methods should handle context cancellation and return descriptive errors
type Destination[T Item] interface {
	// SendMany sends multiple items to the destination in a batch.
	// Implementations can optimize this for bulk operations or simply call Send in a loop.
	SendMany(ctx context.Context, items []T) error

	// Send sends a single item to the destination.
	// If this fails, the item will remain in the source and be retried on the next outbox iteration.
	Send(ctx context.Context, item T) error
}

// Config holds configuration parameters for the outbox processor.
// Zero values will be replaced with sensible defaults.
type Config struct {
	// BatchSize is the number of items to fetch from the source in each iteration.
	// Default: 30
	BatchSize int

	// SleepSec is the number of seconds to sleep when no items are available.
	// This prevents busy-waiting and reduces database load.
	// Default: 5
	SleepSec int

	// MaxConcurrentGroups is the maximum number of entity groups that can be processed concurrently.
	// Higher values increase throughput but also increase resource usage.
	// Should not exceed your database connection pool size.
	// Default: 30
	MaxConcurrentGroups int
}

// Outbox is the main processor that coordinates fetching items from a source,
// sending them to a destination, and tracking success/failure.
//
// Items are automatically grouped by entity ID and processed concurrently up to
// MaxConcurrentGroups. Items within the same entity group are processed sequentially
// to maintain ordering guarantees.
type Outbox[T Item] struct {
	logger      *slog.Logger
	source      Source[T]
	destination Destination[T]
	config      Config
	semaphore   chan struct{}
}

// New creates a new Outbox processor with the given source, destination, configuration, and logger.
//
// The source provides items to process, the destination receives the items,
// and the logger is used for structured logging of all operations.
//
// Config values of 0 or negative will be replaced with defaults:
//   - BatchSize: 30
//   - SleepSec: 5
//   - MaxConcurrentGroups: 30
//
// Example:
//
//	ob := outbox.New(dbSource, mqDestination, outbox.Config{
//	    BatchSize:           50,
//	    SleepSec:            10,
//	    MaxConcurrentGroups: 20,
//	}, slog.Default())
func New[T Item](source Source[T], destination Destination[T], config Config, logger *slog.Logger) *Outbox[T] {
	if config.BatchSize <= 0 {
		config.BatchSize = 30
	}
	if config.SleepSec <= 0 {
		config.SleepSec = 5
	}
	if config.MaxConcurrentGroups <= 0 {
		config.MaxConcurrentGroups = 30
	}

	return &Outbox[T]{
		logger:      logger,
		source:      source,
		destination: destination,
		config:      config,
		semaphore:   make(chan struct{}, config.MaxConcurrentGroups),
	}
}

// Run starts the outbox processor and runs until the context is cancelled.
//
// The processor continuously:
//  1. Fetches a batch of items from the source
//  2. Groups items by entity ID
//  3. Sorts items within each group by sequence (ascending order)
//  4. Processes each group concurrently (up to MaxConcurrentGroups)
//  5. Sends each item to the destination (one attempt per iteration)
//  6. Acknowledges successfully sent items in the source
//  7. Sleeps for SleepSec when no items are available or on errors
//
// Ordering guarantees:
//   - Items with the same entity ID are processed sequentially in sequence order
//   - Items with different entity IDs can be processed concurrently
//   - Sequence is determined by Item.GetSequence() (typically timestamp or version number)
//
// Error handling:
//   - GetItems errors trigger a sleep and retry (prevents hammering the database)
//   - Send errors stop processing for that entity group; failed items remain in source
//   - Acknowledge errors stop processing for that entity group to prevent duplicate sends
//   - Failed items are automatically retried after SleepSec delay
//   - Context cancellation returns ctx.Err()
//
// This method blocks until ctx is cancelled. It's designed to run as a long-lived service.
//
// Example:
//
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//
//	// Handle graceful shutdown
//	go func() {
//	    <-sigterm
//	    cancel()
//	}()
//
//	if err := outbox.Run(ctx); err != context.Canceled {
//	    log.Printf("outbox stopped with error: %v", err)
//	}
func (o *Outbox[T]) Run(ctx context.Context) error {
	o.logger.InfoContext(ctx, "starting outbox with config", slog.Group("config",
		slog.Int("batch_size", o.config.BatchSize),
		slog.Int("max_sleep_sec", o.config.SleepSec),
		slog.Int("max_concurrent_groups", o.config.MaxConcurrentGroups),
	))

	defer func() {
		o.logger.InfoContext(ctx, "outbox stopped")
	}()

	sleep := func() error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(o.config.SleepSec) * time.Second):
			return nil
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			items, err := o.source.GetItems(ctx, o.config.BatchSize)
			if err != nil {
				o.logger.ErrorContext(ctx, "failed to get items", slog.Any("error", err))
				if err := sleep(); err != nil {
					return err
				}
				continue
			}

			if len(items) == 0 {
				o.logger.InfoContext(ctx, "no items to process. sleeping", slog.Int("sleep_sec", o.config.SleepSec))
				if err := sleep(); err != nil {
					return err
				}
				continue
			}

			groupedItems := make(map[string][]T)
			for _, item := range items {
				groupedItems[item.GetEntityId()] = append(groupedItems[item.GetEntityId()], item)
			}

			// Sort items within each group by sequence for ordered processing
			for _, items := range groupedItems {
				slices.SortFunc(items, func(a, b T) int {
					seqA, seqB := a.GetSequence(), b.GetSequence()
					if seqA < seqB {
						return -1
					}
					if seqA > seqB {
						return 1
					}
					return 0
				})
			}

			logger := o.logger.With("run_id", uuid.New().String())
			logger.InfoContext(ctx, "processing items", slog.Int("items", len(items)), slog.Int("groups", len(groupedItems)))

			var wg sync.WaitGroup
			var hadError atomic.Bool

			for groupName, items := range groupedItems {
				groupLogger := logger.With("entity_id", groupName)
				wg.Add(1)
				go func(items []T, l *slog.Logger) {
					defer wg.Done()

					select {
					case o.semaphore <- struct{}{}:
						defer func() { <-o.semaphore }()
					case <-ctx.Done():
						return
					}

					for _, item := range items {
						if err := o.destination.Send(ctx, item); err != nil {
							l.ErrorContext(ctx, "failed to send item", slog.String("item_id", item.GetId()), slog.Any("error", err))
							hadError.Store(true)
							return
						}
						l.DebugContext(ctx, "sent item", slog.String("item_id", item.GetId()))

						if err := o.source.Acknowledge(ctx, item); err != nil {
							l.ErrorContext(ctx, "failed to acknowledge item", slog.String("item_id", item.GetId()), slog.Any("error", err))
							hadError.Store(true)
							return
						}
						l.DebugContext(ctx, "acknowledged item", slog.String("item_id", item.GetId()))
					}
				}(items, groupLogger)
			}
			wg.Wait()

			if hadError.Load() {
				logger.WarnContext(ctx, "batch processing failed, sleeping before retry", slog.Int("sleep_sec", o.config.SleepSec))
				if err := sleep(); err != nil {
					return err
				}
			} else {
				logger.InfoContext(ctx, "items processed successfully")
			}
		}
	}
}

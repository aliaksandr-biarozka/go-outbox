# Go Outbox Pattern

A robust, production-ready implementation of the transactional outbox pattern in Go.

## Overview

The outbox pattern ensures reliable message delivery by persisting events in the same database transaction as your business data, then reliably delivering them to a message broker.

This implementation provides:
- **Reliable delivery** with at-least-once semantics
- **Concurrent processing** with configurable concurrency limits
- **Entity-based grouping** for ordered processing per entity
- **Automatic retries** on errors
- **Comprehensive test coverage** (unit + integration)

## Installation

```bash
go get github.com/aliaksandr-biarozka/go-outbox
```

## Quick Start

```go
package main

import (
    "context"
    "log/slog"
    
    "github.com/aliaksandr-biarozka/go-outbox"
)

// Define your event type
type MyEvent struct {
    ID        string
    EntityID  string
    Sequence  int64
    Payload   []byte
}

func (e *MyEvent) GetId() string        { return e.ID }
func (e *MyEvent) GetEntityId() string  { return e.EntityID }
func (e *MyEvent) GetSequence() int64   { return e.Sequence }

func main() {
    logger := slog.Default()
    
    // Implement Source and Destination interfaces for your event type
    source := &MyDatabaseSource{}
    destination := &MyMessageQueueDestination{}
    
    // Create outbox with your event type
    ob := outbox.New[*MyEvent](source, destination, outbox.Config{
        BatchSize:           30,  // Items to fetch per batch
        SleepSec:            5,   // Sleep when no items
        MaxConcurrentGroups: 30,  // Concurrent entity groups
    }, logger)
    
    // Run the outbox (blocks until context is cancelled)
    ctx := context.Background()
    if err := ob.Run(ctx); err != nil {
        logger.Error("outbox failed", "error", err)
    }
}
```

## Core Interfaces

### Item
```go
type Item interface {
    GetEntityId() string  // Context-specific ID (userId, accountId, etc.)
    GetId() string        // Unique item ID (messageId, orderId, etc.)
    GetSequence() int64   // Monotonically increasing value for ordering
}
```

The `GetSequence()` method determines the processing order within each entity group. Common implementations:
- `return time.Now().UnixMilli()` - Use timestamp in milliseconds
- `return event.CreatedAt.UnixMilli()` - Use database timestamp
- `return event.Version` - Use custom version/sequence number
- `return event.ID` - Use auto-increment database ID

### Source
```go
type Source[T Item] interface {
    GetItems(ctx context.Context, batchSize int) ([]T, error)
    Acknowledge(ctx context.Context, item T) error
}
```

`Source` is generic and works with any type `T` that implements the `Item` interface.

### Destination
```go
type Destination[T Item] interface {
    Send(ctx context.Context, item T) error
}
```

`Destination` is generic and works with any type `T` that implements the `Item` interface.

### Outbox
```go
type Outbox[T Item] struct { ... }

func New[T Item](
    source Source[T],
    destination Destination[T],
    config Config,
    logger *slog.Logger,
) *Outbox[T]
```

The `Outbox` is generic and type-safe. When creating an outbox, specify your event type:
```go
// For pointer types
outbox.New[*MyEvent](source, destination, config, logger)

// For value types
outbox.New[MyEvent](source, destination, config, logger)
```

All interfaces (`Source`, `Destination`) must use the same type `T`. This ensures compile-time type safety and eliminates runtime type assertions.

## How It Works

1. **Fetch**: Retrieves a batch of unsent items from the source (database)
2. **Group**: Groups items by `EntityId` to ensure ordered processing per entity
3. **Send**: Sends each item to the destination (message queue)
4. **Acknowledge**: Marks the item as sent in the source after successful delivery
5. **Repeat**: Continues until context is cancelled

### Entity-Based Processing

Items are grouped by `EntityId` and processed concurrently:
- Items for the same entity are processed **sequentially** in order of `GetSequence()` (maintains order)
- Items for different entities are processed **in parallel** (up to `MaxConcurrentGroups`)

**Ordering Guarantee**: Within each entity group, items are sorted by `GetSequence()` before processing. This ensures that events are delivered in the exact order they were created, even if they're fetched in batches.

## Configuration

```go
type Config struct {
    BatchSize           int      // Number of items to fetch per batch (default: 30)
    SleepSec            int      // Sleep duration when no items (default: 5)
    MaxConcurrentGroups int      // Max concurrent entity groups (default: 30)
    Metrics             Metrics  // Optional metrics collector (default: nil)
}
```

## Observability

### Metrics

The outbox supports optional metrics collection through the `Metrics` interface:

```go
type Metrics interface {
    IncProcessedItems()
    RecordBatchDuration(duration time.Duration, success bool)
}
```

Implement this interface with your preferred metrics backend (Prometheus, OpenTelemetry, StatsD, etc.):

```go
// Example: Prometheus implementation
type prometheusMetrics struct {
    itemsProcessed   prometheus.Counter
    batchDuration    prometheus.Histogram
    batchesSucceeded prometheus.Counter
    batchesFailed    prometheus.Counter
}

func (m *prometheusMetrics) IncProcessedItems() {
    m.itemsProcessed.Inc()
}

func (m *prometheusMetrics) RecordBatchDuration(duration time.Duration, success bool) {
    m.batchDuration.Observe(duration.Seconds())
    if success {
        m.batchesSucceeded.Inc()
    } else {
        m.batchesFailed.Inc()
    }
}

// Use it
metrics := &prometheusMetrics{...}
outbox.New(source, dest, outbox.Config{
    BatchSize: 30,
    Metrics:   metrics,
}, logger)
```

The `IncProcessedItems()` is called once per successfully processed item (after both send and acknowledge). The `RecordBatchDuration()` is called once per batch with the processing time and success status.

## Examples

See [`examples/simple/main.go`](examples/simple/main.go) for a complete example.

For production examples with PostgreSQL and RabbitMQ, see:
- [`integration_test.go`](integration_test.go) - Full integration tests with real databases

## Testing

```bash
# Run unit tests (fast, no Docker needed)
make test-unit

# Run integration tests (requires Docker)
make test-integration

# Run all tests
make test

# Generate coverage report
make test-coverage
```

See [TESTING.md](TESTING.md) for detailed testing documentation.

## Architecture

The `Outbox` is interface-based and doesn't dictate how you implement `Source` or `Destination`. Use any database, message broker, or acknowledgment strategy.

The integration tests demonstrate one common approach (delete after send), but you can implement it however you need.

See [ARCHITECTURE.md](ARCHITECTURE.md) for implementation examples and design considerations.

### Database Schema Example (from integration tests)

```sql
CREATE TABLE outbox_events (
    id VARCHAR(255) PRIMARY KEY,
    entity_id VARCHAR(255) NOT NULL,
    message TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_outbox_events_created_at ON outbox_events(created_at);
```

This is just one possible schema. Your implementation can use any structure that fits your needs.

## Production Considerations

### Error Handling
- Failed sends stop processing for that entity group
- Other entity groups continue processing independently
- Failed items will be retried on the next fetch cycle

### Concurrency
- Limit `MaxConcurrentGroups` based on your database connection pool
- Semaphore ensures controlled concurrency
- Each entity group is processed sequentially

### Monitoring
- All operations are logged with structured logging (slog)
- Each processing run gets a unique `run_id` for tracing
- Failed operations are logged with full error details

### Performance
- Batch processing reduces database round-trips
- Concurrent processing maximizes throughput
- Small table size (delete approach) keeps queries fast

## Dependencies

- [google/uuid](https://github.com/google/uuid) - Run ID generation for logging

## Contributing

Contributions are welcome! Please:
1. Open an issue first to discuss changes
2. Follow Go best practices and coding standards
3. Add tests for new functionality
4. Update documentation as needed

## References

- [Transactional Outbox Pattern](https://microservices.io/patterns/data/transactional-outbox.html)
- [Architecture Documentation](ARCHITECTURE.md)
- [Testing Guide](TESTING.md)


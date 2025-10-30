# Go Outbox Pattern

A robust, production-ready implementation of the transactional outbox pattern in Go.

## Overview

The outbox pattern ensures reliable message delivery by persisting events in the same database transaction as your business data, then reliably delivering them to a message broker.

This implementation provides:
- ✅ **Reliable delivery** with at-least-once semantics
- ✅ **Concurrent processing** with configurable concurrency limits
- ✅ **Automatic retries** with exponential backoff
- ✅ **Entity-based grouping** for ordered processing per entity
- ✅ **Delete-after-send** strategy for optimal performance
- ✅ **Comprehensive test coverage** (unit + integration)

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
    
    "aliaksandr-biarozka/go-outbox"
)

func main() {
    logger := slog.Default()
    
    // Implement Source and Destination interfaces
    source := &MyDatabaseSource{}
    destination := &MyMessageQueueDestination{}
    
    // Create outbox with configuration
    ob := outbox.New(source, destination, outbox.Config{
        BatchSize:           30,  // Items to fetch per batch
        MaxTries:            3,   // Retry attempts
        MaxSleepSec:         5,   // Sleep when no items
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
}
```

### Source
```go
type Source interface {
    GetItems(ctx context.Context, batchSize int) ([]Item, error)
    MarkAsSent(ctx context.Context, item Item) error
}
```

### Destination
```go
type Destination interface {
    Send(ctx context.Context, item Item) error
    SendMany(ctx context.Context, items []Item) error
}
```

## How It Works

1. **Fetch**: Retrieves a batch of unsent items from the source (database)
2. **Group**: Groups items by `EntityId` to ensure ordered processing per entity
3. **Send**: Sends each item to the destination (message queue) with retries
4. **Mark**: Deletes the item from the source after successful delivery
5. **Repeat**: Continues until context is cancelled

### Entity-Based Processing

Items are grouped by `EntityId` and processed concurrently:
- Items for the same entity are processed **sequentially** (maintains order)
- Items for different entities are processed **in parallel** (up to `MaxConcurrentGroups`)

## Configuration

```go
type Config struct {
    BatchSize           int  // Number of items to fetch per batch (default: 30)
    MaxTries            int  // Maximum retry attempts (default: 3)
    MaxSleepSec         int  // Sleep duration when no items (default: 5)
    MaxConcurrentGroups int  // Max concurrent entity groups (default: 30)
}
```

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

### Delete vs Soft Delete

This implementation uses the **delete approach** - events are deleted immediately after successful delivery.

**Why delete?**
- ✅ Small table size = optimal query performance
- ✅ No cleanup jobs needed
- ✅ Minimal indexing required
- ✅ Simple implementation

See [ARCHITECTURE.md](ARCHITECTURE.md) for detailed design decisions and alternative approaches.

### Database Schema Example

```sql
CREATE TABLE outbox_events (
    id VARCHAR(255) PRIMARY KEY,
    entity_id VARCHAR(255) NOT NULL,
    message TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_outbox_events_created_at ON outbox_events(created_at);
```

## Production Considerations

### Error Handling
- Failed sends are retried with exponential backoff
- If all retries fail, processing stops for that entity group
- Other entity groups continue processing

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

- [cenkalti/backoff/v5](https://github.com/cenkalti/backoff) - Exponential backoff for retries
- [google/uuid](https://github.com/google/uuid) - Run ID generation

## Contributing

Contributions are welcome! Please:
1. Open an issue first to discuss changes
2. Follow Go best practices and coding standards
3. Add tests for new functionality
4. Update documentation as needed

## License

[Add your license here]

## References

- [Transactional Outbox Pattern](https://microservices.io/patterns/data/transactional-outbox.html)
- [Architecture Documentation](ARCHITECTURE.md)
- [Testing Guide](TESTING.md)


# Architecture

## Core Design

The `Outbox` is interface-based and doesn't prescribe any specific database or message broker. Implement `Source` and `Destination` however you need.

This document shows how the integration tests implement `Source`, but these are just examples.

## Event Acknowledgment Strategies

### Delete After Send

Integration tests use this approach - events are deleted once delivered.

```go
func (p *postgresSource) Acknowledge(ctx context.Context, item *outboxEvent) error {
    query := `DELETE FROM outbox_events WHERE id = $1`
    _, err := p.db.ExecContext(ctx, query, item.GetId())
    return err
}
```

Pros:
- Simple
- Fast queries (table stays small)
- No maintenance needed

Cons:
- No audit trail
- Can't replay events

### Soft Delete

Mark events as sent instead of deleting them.

```go
func (p *postgresSource) Acknowledge(ctx context.Context, item *outboxEvent) error {
    query := `UPDATE outbox_events SET sent_at = NOW() WHERE id = $1`
    _, err := p.db.ExecContext(ctx, query, item.GetId())
    return err
}
```

Requires:
- `sent_at TIMESTAMP NULL` column
- Partial index: `CREATE INDEX idx_outbox_events_unsent ON outbox_events(created_at) WHERE sent_at IS NULL`
- Cleanup job to purge old events

## Schema Example

```sql
CREATE TABLE outbox_events (
    id VARCHAR(255) PRIMARY KEY,
    entity_id VARCHAR(255) NOT NULL,
    sequence BIGINT NOT NULL,
    message TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_outbox_events_sequence ON outbox_events(sequence);
```

## Event Ordering

**Don't use `created_at` for ordering in production.** Multiple events created in the same transaction will have identical (or nearly identical) timestamps. Clock skew between servers makes this worse.

Use an explicit sequence number instead:

```go
func CreateOrder(ctx context.Context, tx *sql.Tx, order Order) error {
    seq := 1
    
    if err := saveOrder(tx, order); err != nil {
        return err
    }
    
    events := []OutboxEvent{
        {EntityID: order.ID, Sequence: seq++, Type: "OrderCreated"},
        {EntityID: order.ID, Sequence: seq++, Type: "OrderItemAdded"},
        {EntityID: order.ID, Sequence: seq++, Type: "OrderConfirmed"},
    }
    return saveOutboxEvents(tx, events)
}
```

The `GetSequence()` method on the `Item` interface exists for this reason.

## Migration to Soft Delete

If you start with delete-after-send and later need audit capabilities:

1. Add `sent_at` column
2. Update `Acknowledge` to set `sent_at` instead of deleting
3. Create partial index: `CREATE INDEX ... WHERE sent_at IS NULL`
4. Add cleanup job to purge old events
5. Update queries to filter `WHERE sent_at IS NULL`

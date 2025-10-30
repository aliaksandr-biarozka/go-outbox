# Outbox Pattern Implementation Summary

## Delete vs Soft Delete Approach

This implementation uses the **delete approach** for the outbox pattern, which is the most common and recommended approach for production systems.

## Why Delete Instead of Soft Delete?

### Delete Approach (Current Implementation)
```go
func (p *postgresSource) MarkAsSent(ctx context.Context, item Item) error {
    query := `DELETE FROM outbox_events WHERE id = $1`
    _, err := p.db.ExecContext(ctx, query, item.GetId())
    return err
}
```

**Advantages:**
- ✅ Optimal performance - table stays small
- ✅ Minimal indexing required (just primary key + created_at)
- ✅ No cleanup jobs needed
- ✅ Simple to implement and maintain
- ✅ No risk of unbounded table growth

**Trade-offs:**
- ❌ No audit trail of sent events
- ❌ Cannot replay historical events
- ❌ Cannot debug past deliveries

### Soft Delete Approach (Alternative)
```go
func (p *postgresSource) MarkAsSent(ctx context.Context, item Item) error {
    query := `UPDATE outbox_events SET sent_at = NOW() WHERE id = $1`
    _, err := p.db.ExecContext(ctx, query, item.GetId())
    return err
}
```

Requires:
- `sent_at TIMESTAMP NULL` column
- Partial indexes: `CREATE INDEX ... WHERE sent_at IS NULL`
- Cleanup job to delete old events
- More complex queries

## Database Schema

### Current (Delete Approach)
```sql
CREATE TABLE outbox_events (
    id VARCHAR(255) PRIMARY KEY,
    entity_id VARCHAR(255) NOT NULL,
    message TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_outbox_events_created_at ON outbox_events(created_at);
```

### Query
```sql
SELECT id, entity_id, message, created_at 
FROM outbox_events 
ORDER BY created_at ASC 
LIMIT $1
```

## When to Use Each Approach

### Use Delete (Current) When:
- You don't need audit trail
- Performance is critical
- Simplicity is valued
- Event replay not required
- No compliance requirements for event retention

### Use Soft Delete When:
- Audit trail required (e.g., compliance, regulations)
- Need to debug delivery issues
- Want to replay events
- Need analytics on sent events
- Can tolerate cleanup job complexity

### Use Archive Approach When:
- Need full history but also fast queries
- Analytics on historical data needed
- Compliance requires long-term retention
- Have resources for more complex setup

## Migration Path

If you later need audit capabilities, you can easily migrate:

1. **Add sent_at column:**
```sql
ALTER TABLE outbox_events ADD COLUMN sent_at TIMESTAMP NULL;
```

2. **Update MarkAsSent to soft delete:**
```go
func (p *postgresSource) MarkAsSent(ctx context.Context, item Item) error {
    query := `UPDATE outbox_events SET sent_at = NOW() WHERE id = $1`
    _, err := p.db.ExecContext(ctx, query, item.GetId())
    return err
}
```

3. **Add partial indexes:**
```sql
CREATE INDEX idx_outbox_events_unsent ON outbox_events(created_at) WHERE sent_at IS NULL;
```

4. **Update GetItems query:**
```sql
SELECT id, entity_id, message, created_at 
FROM outbox_events 
WHERE sent_at IS NULL
ORDER BY created_at ASC 
LIMIT $1
```

5. **Add cleanup job:**
```go
func CleanupOldEvents(ctx context.Context, db *sql.DB, retentionDays int) error {
    query := `DELETE FROM outbox_events WHERE sent_at < NOW() - INTERVAL '? days'`
    _, err := db.ExecContext(ctx, query, retentionDays)
    return err
}
```

## Recommendations

**For most use cases:** Use the current delete approach. It's battle-tested, performant, and simple.

**For compliance-heavy systems:** Use soft delete with 7-30 day retention and a daily cleanup job.

**For enterprise analytics:** Use archive table approach to separate hot (unsent) from cold (historical) data.

## Performance Characteristics

### Delete Approach
- **Write performance:** Excellent (simple DELETE)
- **Read performance:** Excellent (small table, simple index)
- **Storage:** Minimal (only unsent events)
- **Maintenance:** None required

### Soft Delete with Cleanup
- **Write performance:** Good (UPDATE + periodic DELETE)
- **Read performance:** Good (with partial indexes)
- **Storage:** Moderate (retention period * event rate)
- **Maintenance:** Daily cleanup job

### Archive Approach
- **Write performance:** Good (INSERT into archive)
- **Read performance:** Excellent for hot table, slower for archive
- **Storage:** High (full history)
- **Maintenance:** Archive job + separate table management


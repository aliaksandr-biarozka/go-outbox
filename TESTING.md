# Testing Guide

This project includes comprehensive unit and integration tests for the outbox pattern implementation.

## Test Structure

### Unit Tests (`outbox_test.go`)
- Test the `Outbox` struct and its methods in isolation
- Use mock implementations for `Source` and `Destination` interfaces
- Cover various scenarios including error handling, batching, and concurrency

### Integration Tests (`integration_test.go`)
- Test the outbox with real PostgreSQL and RabbitMQ
- Use testcontainers to spin up temporary database and message queue instances
- Verify end-to-end functionality including message persistence and delivery

## Running Tests

### Prerequisites
- Go 1.25.1 or later
- Docker (Docker Desktop, Colima, or standard Docker)

**Note for Colima users:** The Makefile automatically detects and uses the correct Docker socket. No manual configuration needed.

### Running Tests in IDE (Cursor/VS Code)

The `.vscode/settings.json` file is pre-configured with the correct Docker socket path for Colima. If you're using a different Docker setup, update the file:

```json
{
    "go.testEnvVars": {
        "DOCKER_HOST": "unix:///path/to/your/docker.sock",
        "TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE": "/var/run/docker.sock"
    }
}
```

Common Docker socket paths:
- **Colima**: `unix:///Users/<username>/.colima/default/docker.sock`
- **Docker Desktop**: `unix:///Users/<username>/.docker/run/docker.sock`
- **Standard Docker (Linux)**: `unix:///var/run/docker.sock`

### Quick Start
```bash
# Run all tests
make test

# Run only unit tests (fast)
make test-unit

# Run only integration tests
make test-integration

# Run tests with coverage
make test-coverage
```

### Using External Services
If you prefer to use external PostgreSQL and RabbitMQ services instead of testcontainers:

```bash
# Start external services
make test-services-up

# Run integration tests
make test-integration-external

# Stop external services
make test-services-down
```

## Test Configuration

### Unit Tests
Unit tests use mock implementations and run quickly without external dependencies.

### Integration Tests
Integration tests use testcontainers by default, which automatically manages Docker containers for:
- PostgreSQL 15 (for the source database)
- RabbitMQ 3 (for the destination message queue)

The integration tests create a temporary database schema and verify:
- Message persistence in PostgreSQL
- Message delivery to RabbitMQ
- Error handling and retry logic
- Concurrent processing of multiple entity groups

## Test Data

### Database Schema
The integration tests create an `outbox_events` table with the following structure:
```sql
CREATE TABLE outbox_events (
    id VARCHAR(255) PRIMARY KEY,
    entity_id VARCHAR(255) NOT NULL,
    message TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_outbox_events_created_at ON outbox_events(created_at);
```

**Note:** The implementation uses the **delete approach** - events are deleted from the table immediately after successful delivery. This is the most common pattern for production systems as it:
- Keeps the table small for optimal performance
- Requires minimal indexing (just the primary key and created_at for ordering)
- Eliminates the need for cleanup jobs
- Provides the best query performance

If you need an audit trail, consider:
- Adding a retention period with a cleanup job (soft delete with `sent_at` column)
- Archiving to a separate table
- Logging to an external system

### Message Format
Test messages are JSON objects with the following structure:
```json
{
    "type": "user_created",
    "data": {
        "id": "user1"
    }
}
```

## Test Scenarios

### Unit Tests
1. **Constructor validation** - Ensures default values are set correctly
2. **No items processing** - Handles empty source gracefully
3. **Item processing** - Processes items and marks them as sent
4. **Context cancellation** - Stops processing when context is cancelled
5. **Source errors** - Handles source errors gracefully
6. **Destination errors** - Handles destination errors with retry logic
7. **Item grouping** - Groups items by entity ID for concurrent processing
8. **Batch size limits** - Respects batch size configuration

### Integration Tests
1. **End-to-end processing** - Verifies complete message flow from database to message queue
2. **Error handling** - Tests behavior when destination is unavailable
3. **Concurrent processing** - Verifies multiple entity groups are processed concurrently
4. **Message persistence** - Ensures messages are marked as sent after successful delivery

## Debugging Tests

### Enable Debug Logging
Set the log level to debug in test files:
```go
logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
    Level: slog.LevelDebug,
}))
```

### View Test Coverage
```bash
make test-coverage
# Opens coverage.html in your browser
```

### Run Specific Tests
```bash
# Run a specific test
go test -v -run TestOutbox_Run_WithItems

# Run integration tests only
go test -v -run TestIntegration

# Run with verbose output
go test -v -count=1 ./...
```

## Continuous Integration

The tests are designed to run in CI environments:
- Unit tests run without external dependencies
- Integration tests use testcontainers for consistent environment
- All tests are deterministic and don't rely on external state

## Troubleshooting

### Common Issues

1. **Docker not running** - Ensure Docker is running for integration tests
   ```bash
   docker ps  # Should show running containers without error
   ```

2. **"Cannot connect to Docker daemon" error in IDE**
   - Make sure `.vscode/settings.json` has the correct `DOCKER_HOST` path
   - Verify your Docker socket path: `ls -la ~/.colima/default/docker.sock` (for Colima)
   - Restart Cursor/VS Code after changing settings

3. **Port conflicts** - Integration tests use random ports, but external services use 5432 and 5672

4. **Test timeouts** - Integration tests can take 30-60 seconds due to container startup
   - The timeout is set to 120s in `.vscode/settings.json`

5. **Resource limits** - Ensure sufficient memory for Docker containers (at least 2GB)

### Test Dependencies
- `github.com/lib/pq` - PostgreSQL driver
- `github.com/streadway/amqp` - RabbitMQ client
- `github.com/testcontainers/testcontainers-go` - Container management
- `github.com/testcontainers/testcontainers-go/modules/postgres` - PostgreSQL module
- `github.com/testcontainers/testcontainers-go/modules/rabbitmq` - RabbitMQ module

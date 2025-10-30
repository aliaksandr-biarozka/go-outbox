package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/streadway/amqp"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/modules/rabbitmq"
)

type postgresSource struct {
	db     *sql.DB
	logger *slog.Logger
}

func newPostgresSource(db *sql.DB, logger *slog.Logger) *postgresSource {
	return &postgresSource{
		db:     db,
		logger: logger,
	}
}

func (p *postgresSource) GetItems(ctx context.Context, batchSize int) ([]*outboxEvent, error) {
	query := `
		SELECT id, entity_id, message, created_at 
		FROM outbox_events 
		ORDER BY created_at ASC 
		LIMIT $1
	`

	rows, err := p.db.QueryContext(ctx, query, batchSize)
	if err != nil {
		return nil, fmt.Errorf("failed to query outbox events: %w", err)
	}
	defer rows.Close()

	var items []*outboxEvent
	for rows.Next() {
		var event outboxEvent
		err := rows.Scan(&event.id, &event.entityId, &event.message, &event.createdAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan outbox event: %w", err)
		}
		items = append(items, &event)
	}

	return items, nil
}

func (p *postgresSource) Acknowledge(ctx context.Context, item *outboxEvent) error {
	query := `DELETE FROM outbox_events WHERE id = $1`
	_, err := p.db.ExecContext(ctx, query, item.GetId())
	if err != nil {
		return fmt.Errorf("failed to delete sent event: %w", err)
	}
	return nil
}

type rabbitmqDestination struct {
	conn   *amqp.Connection
	ch     *amqp.Channel
	logger *slog.Logger
}

func newRabbitmqDestination(conn *amqp.Connection, logger *slog.Logger) (*rabbitmqDestination, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("failed to open channel: %w", err)
	}

	err = ch.ExchangeDeclare(
		"outbox", // name
		"topic",  // type
		true,     // durable
		false,    // auto-deleted
		false,    // internal
		false,    // no-wait
		nil,      // arguments
	)
	if err != nil {
		return nil, fmt.Errorf("failed to declare exchange: %w", err)
	}

	return &rabbitmqDestination{
		conn:   conn,
		ch:     ch,
		logger: logger,
	}, nil
}

func (r *rabbitmqDestination) Send(ctx context.Context, item *outboxEvent) error {
	routingKey := fmt.Sprintf("entity.%s", item.GetEntityId())

	err := r.ch.Publish(
		"outbox",   // exchange
		routingKey, // routing key
		false,      // mandatory
		false,      // immediate
		amqp.Publishing{
			ContentType: "application/json",
			Body:        []byte(item.message),
			MessageId:   item.id,
			Timestamp:   item.createdAt,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to publish message: %w", err)
	}

	return nil
}

func (r *rabbitmqDestination) SendMany(ctx context.Context, items []*outboxEvent) error {
	for _, item := range items {
		if err := r.Send(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (r *rabbitmqDestination) Close() error {
	if r.ch != nil {
		r.ch.Close()
	}
	if r.conn != nil {
		r.conn.Close()
	}
	return nil
}

type outboxEvent struct {
	id        string
	entityId  string
	message   string
	createdAt time.Time
}

func (e *outboxEvent) GetEntityId() string { return e.entityId }
func (e *outboxEvent) GetId() string       { return e.id }
func (e *outboxEvent) GetSequence() int64  { return e.createdAt.UnixMilli() }

func setupPostgres(t *testing.T) (*sql.DB, func()) {
	ctx := context.Background()

	postgresContainer, err := postgres.RunContainer(ctx,
		testcontainers.WithImage("postgres:15-alpine"),
		postgres.WithDatabase("outbox_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
	)
	if err != nil {
		t.Fatalf("failed to start postgres container: %v", err)
	}

	connStr, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("failed to get postgres connection string: %v", err)
	}

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		t.Fatalf("failed to connect to postgres: %v", err)
	}

	for i := 0; i < 10; i++ {
		err = db.Ping()
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("failed to ping postgres after retries: %v", err)
	}

	createTableSQL := `
		CREATE TABLE IF NOT EXISTS outbox_events (
			id VARCHAR(255) PRIMARY KEY,
			entity_id VARCHAR(255) NOT NULL,
			message TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_outbox_events_created_at ON outbox_events(created_at);
	`

	_, err = db.Exec(createTableSQL)
	if err != nil {
		t.Fatalf("failed to create outbox_events table: %v", err)
	}

	cleanup := func() {
		db.Close()
		postgresContainer.Terminate(ctx)
	}

	return db, cleanup
}

func setupRabbitMQ(t *testing.T) (*amqp.Connection, func()) {
	ctx := context.Background()

	rabbitmqContainer, err := rabbitmq.RunContainer(ctx,
		testcontainers.WithImage("rabbitmq:3-management-alpine"),
	)
	if err != nil {
		t.Fatalf("failed to start rabbitmq container: %v", err)
	}

	connStr, err := rabbitmqContainer.AmqpURL(ctx)
	if err != nil {
		t.Fatalf("failed to get rabbitmq connection string: %v", err)
	}

	var conn *amqp.Connection
	maxRetries := 60
	for i := 0; i < maxRetries; i++ {
		conn, err = amqp.Dial(connStr)
		if err == nil {
			break
		}
		if i == maxRetries-1 {
			t.Fatalf("failed to connect to rabbitmq after %d retries: %v", maxRetries, err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	cleanup := func() {
		conn.Close()
		rabbitmqContainer.Terminate(ctx)
	}

	return conn, cleanup
}

func bulkInsertEvents(t *testing.T, db *sql.DB, events []struct {
	id       string
	entityId string
	message  string
}) {
	if len(events) == 0 {
		return
	}

	query := "INSERT INTO outbox_events (id, entity_id, message, created_at) VALUES "
	values := make([]interface{}, 0, len(events)*3)
	placeholders := make([]string, 0, len(events))

	for i, event := range events {
		placeholders = append(placeholders, fmt.Sprintf("($%d, $%d, $%d, NOW())", i*3+1, i*3+2, i*3+3))
		values = append(values, event.id, event.entityId, event.message)
	}

	query += strings.Join(placeholders, ", ")

	_, err := db.Exec(query, values...)
	if err != nil {
		t.Fatalf("failed to bulk insert test events: %v", err)
	}
}

func TestIntegration_OutboxWithPostgresAndRabbitMQ(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	db, cleanupDB := setupPostgres(t)
	defer cleanupDB()

	rabbitConn, cleanupRabbit := setupRabbitMQ(t)
	defer cleanupRabbit()

	source := newPostgresSource(db, logger)
	destination, err := newRabbitmqDestination(rabbitConn, logger)
	if err != nil {
		t.Fatalf("failed to create rabbitmq destination: %v", err)
	}
	defer destination.Close()

	outbox := New[*outboxEvent](source, destination, Config{
		BatchSize:           5,
		SleepSec:            1,
		MaxConcurrentGroups: 2,
	}, logger)

	events := []struct {
		id       string
		entityId string
		message  string
	}{
		{"event1", "user1", `{"type": "user_created", "data": {"id": "user1"}}`},
		{"event2", "user1", `{"type": "user_updated", "data": {"id": "user1"}}`},
		{"event3", "user2", `{"type": "user_created", "data": {"id": "user2"}}`},
		{"event4", "user2", `{"type": "user_deleted", "data": {"id": "user2"}}`},
		{"event5", "user3", `{"type": "user_created", "data": {"id": "user3"}}`},
	}

	bulkInsertEvents(t, db, events)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- outbox.Run(ctx)
	}()

	time.Sleep(2 * time.Second)
	cancel()

	<-errChan

	var remainingCount int
	err = db.QueryRow("SELECT COUNT(*) FROM outbox_events").Scan(&remainingCount)
	if err != nil {
		t.Fatalf("failed to query remaining events count: %v", err)
	}

	if remainingCount != 0 {
		t.Errorf("expected 0 remaining events (allgo env GOMODCACHE deleted), got %d", remainingCount)
	}
}

func TestIntegration_OutboxWithPostgresAndRabbitMQ_ErrorHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	db, cleanupDB := setupPostgres(t)
	defer cleanupDB()

	rabbitConn, cleanupRabbit := setupRabbitMQ(t)
	defer cleanupRabbit()

	source := newPostgresSource(db, logger)
	destination, err := newRabbitmqDestination(rabbitConn, logger)
	if err != nil {
		t.Fatalf("failed to create rabbitmq destination: %v", err)
	}
	defer destination.Close()

	outbox := New[*outboxEvent](source, destination, Config{
		BatchSize:           5,
		SleepSec:            1,
		MaxConcurrentGroups: 2,
	}, logger)

	events := []struct {
		id       string
		entityId string
		message  string
	}{
		{"event1", "user1", `{"type": "user_created", "data": {"id": "user1"}}`},
		{"event2", "user1", `{"type": "user_updated", "data": {"id": "user1"}}`},
	}

	bulkInsertEvents(t, db, events)

	rabbitConn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- outbox.Run(ctx)
	}()

	time.Sleep(2 * time.Second)
	cancel()

	<-errChan

	var remainingCount int
	err = db.QueryRow("SELECT COUNT(*) FROM outbox_events").Scan(&remainingCount)
	if err != nil {
		t.Fatalf("failed to query remaining events count: %v", err)
	}

	if remainingCount != len(events) {
		t.Errorf("expected %d events to remain (not deleted due to destination failure), got %d", len(events), remainingCount)
	}
}

func TestIntegration_OutboxWithPostgresAndRabbitMQ_ConcurrentProcessing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	db, cleanupDB := setupPostgres(t)
	defer cleanupDB()

	rabbitConn, cleanupRabbit := setupRabbitMQ(t)
	defer cleanupRabbit()

	source := newPostgresSource(db, logger)
	destination, err := newRabbitmqDestination(rabbitConn, logger)
	if err != nil {
		t.Fatalf("failed to create rabbitmq destination: %v", err)
	}
	defer destination.Close()

	outbox := New[*outboxEvent](source, destination, Config{
		BatchSize:           3,
		SleepSec:            1,
		MaxConcurrentGroups: 1, // Only 1 concurrent group
	}, logger)

	events := make([]struct {
		id       string
		entityId string
		message  string
	}, 0, 20)

	for i := 0; i < 20; i++ {
		entityId := fmt.Sprintf("user%d", i%5) // 5 different entities
		events = append(events, struct {
			id       string
			entityId string
			message  string
		}{
			id:       fmt.Sprintf("event%d", i),
			entityId: entityId,
			message:  fmt.Sprintf(`{"type": "user_event", "data": {"id": "%s", "index": %d}}`, entityId, i),
		})
	}

	bulkInsertEvents(t, db, events)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- outbox.Run(ctx)
	}()

	time.Sleep(5 * time.Second)
	cancel()

	<-errChan

	var remainingCount int
	err = db.QueryRow("SELECT COUNT(*) FROM outbox_events").Scan(&remainingCount)
	if err != nil {
		t.Fatalf("failed to query remaining events count: %v", err)
	}

	if remainingCount != 0 {
		t.Errorf("expected 0 remaining events (all deleted), got %d", remainingCount)
	}
}

func TestIntegration_OutboxSequenceOrdering(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	db, cleanupDB := setupPostgres(t)
	defer cleanupDB()

	rabbitConn, cleanupRabbit := setupRabbitMQ(t)
	defer cleanupRabbit()

	source := newPostgresSource(db, logger)
	destination, err := newRabbitmqDestination(rabbitConn, logger)
	if err != nil {
		t.Fatalf("failed to create rabbitmq destination: %v", err)
	}
	defer destination.Close()

	ch, err := rabbitConn.Channel()
	if err != nil {
		t.Fatalf("failed to open channel: %v", err)
	}
	defer ch.Close()

	queue, err := ch.QueueDeclare("", false, false, false, false, nil)
	if err != nil {
		t.Fatalf("failed to declare queue: %v", err)
	}

	err = ch.QueueBind(queue.Name, "entity.*", "outbox", false, nil)
	if err != nil {
		t.Fatalf("failed to bind queue: %v", err)
	}

	outbox := New[*outboxEvent](source, destination, Config{
		BatchSize:           10,
		SleepSec:            1,
		MaxConcurrentGroups: 2,
	}, logger)

	now := time.Now()
	events := []struct {
		id       string
		entityId string
		message  string
		sequence int64
	}{
		{"event1", "user1", `{"type": "created", "seq": 3}`, now.Add(3 * time.Second).UnixMilli()},
		{"event2", "user1", `{"type": "created", "seq": 1}`, now.Add(1 * time.Second).UnixMilli()},
		{"event3", "user1", `{"type": "created", "seq": 2}`, now.Add(2 * time.Second).UnixMilli()},
		{"event4", "user2", `{"type": "created", "seq": 20}`, now.Add(20 * time.Second).UnixMilli()},
		{"event5", "user2", `{"type": "created", "seq": 10}`, now.Add(10 * time.Second).UnixMilli()},
	}

	query := "INSERT INTO outbox_events (id, entity_id, message, created_at) VALUES "
	values := make([]interface{}, 0, len(events)*4)
	placeholders := make([]string, 0, len(events))

	for i, event := range events {
		createdAt := time.UnixMilli(event.sequence)
		placeholders = append(placeholders, fmt.Sprintf("($%d, $%d, $%d, $%d)", i*4+1, i*4+2, i*4+3, i*4+4))
		values = append(values, event.id, event.entityId, event.message, createdAt)
	}

	query += strings.Join(placeholders, ", ")
	_, err = db.Exec(query, values...)
	if err != nil {
		t.Fatalf("failed to insert test events: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- outbox.Run(ctx)
	}()

	time.Sleep(3 * time.Second)
	cancel()

	<-errChan

	var remainingCount int
	err = db.QueryRow("SELECT COUNT(*) FROM outbox_events").Scan(&remainingCount)
	if err != nil {
		t.Fatalf("failed to query remaining events count: %v", err)
	}

	if remainingCount != 0 {
		t.Errorf("expected 0 remaining events (all deleted), got %d", remainingCount)
	}

	type message struct {
		body     string
		entityId string
	}
	messages := make([]message, 0)
	for {
		msg, ok, err := ch.Get(queue.Name, true)
		if err != nil || !ok {
			break
		}
		routingKey := msg.RoutingKey
		entityId := ""
		if len(routingKey) > 7 {
			entityId = routingKey[7:]
		}
		messages = append(messages, message{body: string(msg.Body), entityId: entityId})
	}

	if len(messages) != 5 {
		t.Errorf("expected 5 messages, got %d", len(messages))
	}

	user1Messages := make([]string, 0)
	user2Messages := make([]string, 0)
	for _, msg := range messages {
		if msg.entityId == "user1" {
			user1Messages = append(user1Messages, msg.body)
		} else if msg.entityId == "user2" {
			user2Messages = append(user2Messages, msg.body)
		}
	}

	if len(user1Messages) != 3 {
		t.Errorf("expected 3 user1 messages, got %d", len(user1Messages))
	}
	if len(user2Messages) != 2 {
		t.Errorf("expected 2 user2 messages, got %d", len(user2Messages))
	}

	if len(user1Messages) == 3 {
		if user1Messages[0] != `{"type": "created", "seq": 1}` {
			t.Errorf("user1 first message incorrect: %s", user1Messages[0])
		}
		if user1Messages[1] != `{"type": "created", "seq": 2}` {
			t.Errorf("user1 second message incorrect: %s", user1Messages[1])
		}
		if user1Messages[2] != `{"type": "created", "seq": 3}` {
			t.Errorf("user1 third message incorrect: %s", user1Messages[2])
		}
	}

	if len(user2Messages) == 2 {
		if user2Messages[0] != `{"type": "created", "seq": 10}` {
			t.Errorf("user2 first message incorrect: %s", user2Messages[0])
		}
		if user2Messages[1] != `{"type": "created", "seq": 20}` {
			t.Errorf("user2 second message incorrect: %s", user2Messages[1])
		}
	}
}

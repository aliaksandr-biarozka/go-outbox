package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

type mockItem struct {
	entityId string
	id       string
}

func (m mockItem) GetEntityId() string { return m.entityId }
func (m mockItem) GetId() string       { return m.id }

type mockSource struct {
	items           []Item
	markAsSentCalls []Item
	mu              sync.RWMutex
	shouldError     bool
	errorMsg        string
}

func newMockSource() *mockSource {
	return &mockSource{
		items:           make([]Item, 0),
		markAsSentCalls: make([]Item, 0),
	}
}

func (m *mockSource) GetItems(ctx context.Context, batchSize int) ([]Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.shouldError {
		return nil, errors.New(m.errorMsg)
	}

	if len(m.items) == 0 {
		return []Item{}, nil
	}

	var result []Item
	if len(m.items) > batchSize {
		result = m.items[:batchSize]
		m.items = m.items[batchSize:]
	} else {
		result = m.items
		m.items = []Item{}
	}
	return result, nil
}

func (m *mockSource) MarkAsSent(ctx context.Context, item Item) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.shouldError {
		return errors.New(m.errorMsg)
	}

	m.markAsSentCalls = append(m.markAsSentCalls, item)
	return nil
}

func (m *mockSource) setItems(items []Item) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = items
}

func (m *mockSource) setError(shouldError bool, errorMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shouldError = shouldError
	m.errorMsg = errorMsg
}

func (m *mockSource) getMarkAsSentCalls() []Item {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Item(nil), m.markAsSentCalls...)
}

type mockDestination struct {
	sendCalls     []Item
	sendManyCalls [][]Item
	mu            sync.RWMutex
	shouldError   bool
	errorMsg      string
}

func newMockDestination() *mockDestination {
	return &mockDestination{
		sendCalls:     make([]Item, 0),
		sendManyCalls: make([][]Item, 0),
	}
}

func (m *mockDestination) Send(ctx context.Context, item Item) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sendCalls = append(m.sendCalls, item)

	if m.shouldError {
		return errors.New(m.errorMsg)
	}

	return nil
}

func (m *mockDestination) SendMany(ctx context.Context, items []Item) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.shouldError {
		return errors.New(m.errorMsg)
	}

	m.sendManyCalls = append(m.sendManyCalls, items)
	return nil
}

func (m *mockDestination) setError(shouldError bool, errorMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shouldError = shouldError
	m.errorMsg = errorMsg
}

func (m *mockDestination) getSendCalls() []Item {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Item(nil), m.sendCalls...)
}

func (m *mockDestination) getSendManyCalls() [][]Item {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([][]Item(nil), m.sendManyCalls...)
}

func TestNew(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	source := newMockSource()
	destination := newMockDestination()

	tests := []struct {
		name     string
		config   Config
		expected Config
	}{
		{
			name:   "default values when config is zero",
			config: Config{},
			expected: Config{
				BatchSize:           30,
				MaxTries:            3,
				MaxSleepSec:         5,
				MaxConcurrentGroups: 30,
			},
		},
		{
			name: "custom values",
			config: Config{
				BatchSize:           10,
				MaxTries:            5,
				MaxSleepSec:         10,
				MaxConcurrentGroups: 5,
			},
			expected: Config{
				BatchSize:           10,
				MaxTries:            5,
				MaxSleepSec:         10,
				MaxConcurrentGroups: 5,
			},
		},
		{
			name: "negative values should be set to defaults",
			config: Config{
				BatchSize:           -1,
				MaxTries:            -1,
				MaxSleepSec:         -1,
				MaxConcurrentGroups: -1,
			},
			expected: Config{
				BatchSize:           30,
				MaxTries:            3,
				MaxSleepSec:         5,
				MaxConcurrentGroups: 30,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outbox := New(source, destination, tt.config, logger)

			if outbox.config != tt.expected {
				t.Errorf("expected config %+v, got %+v", tt.expected, outbox.config)
			}

			if outbox.source != source {
				t.Error("source not set correctly")
			}

			if outbox.destination != destination {
				t.Error("destination not set correctly")
			}

			if outbox.logger != logger {
				t.Error("logger not set correctly")
			}

			if cap(outbox.semaphore) != tt.expected.MaxConcurrentGroups {
				t.Errorf("expected semaphore capacity %d, got %d", tt.expected.MaxConcurrentGroups, cap(outbox.semaphore))
			}
		})
	}
}

func TestOutbox_Run_NoItems(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	source := newMockSource()
	destination := newMockDestination()

	outbox := New(source, destination, Config{
		BatchSize:           10,
		MaxTries:            3,
		MaxSleepSec:         1, // Short sleep for testing
		MaxConcurrentGroups: 5,
	}, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- outbox.Run(ctx)
	}()

	<-ctx.Done()

	sendCalls := destination.getSendCalls()
	if len(sendCalls) != 0 {
		t.Errorf("expected 0 send calls, got %d", len(sendCalls))
	}
}

func TestOutbox_Run_WithItems(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	source := newMockSource()
	destination := newMockDestination()

	items := []Item{
		mockItem{entityId: "user1", id: "item1"},
		mockItem{entityId: "user1", id: "item2"},
		mockItem{entityId: "user2", id: "item3"},
	}
	source.setItems(items)

	outbox := New(source, destination, Config{
		BatchSize:           10,
		MaxTries:            3,
		MaxSleepSec:         1,
		MaxConcurrentGroups: 5,
	}, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- outbox.Run(ctx)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	<-errChan

	sendCalls := destination.getSendCalls()
	if len(sendCalls) != len(items) {
		t.Errorf("expected %d send calls, got %d", len(items), len(sendCalls))
	}

	markAsSentCalls := source.getMarkAsSentCalls()
	if len(markAsSentCalls) != len(items) {
		t.Errorf("expected %d mark as sent calls, got %d", len(items), len(markAsSentCalls))
	}
}

func TestOutbox_Run_ContextCancellation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	source := newMockSource()
	destination := newMockDestination()

	outbox := New(source, destination, Config{
		BatchSize:           10,
		MaxTries:            3,
		MaxSleepSec:         1,
		MaxConcurrentGroups: 5,
	}, logger)

	ctx, cancel := context.WithCancel(context.Background())

	errChan := make(chan error, 1)
	go func() {
		errChan <- outbox.Run(ctx)
	}()

	cancel()

	err := <-errChan

	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestOutbox_Run_SourceError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	source := newMockSource()
	destination := newMockDestination()

	source.setError(true, "source error")

	outbox := New(source, destination, Config{
		BatchSize:           10,
		MaxTries:            3,
		MaxSleepSec:         1,
		MaxConcurrentGroups: 5,
	}, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- outbox.Run(ctx)
	}()

	err := <-errChan

	if err != context.DeadlineExceeded {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}

	sendCalls := destination.getSendCalls()
	if len(sendCalls) != 0 {
		t.Errorf("expected 0 send calls, got %d", len(sendCalls))
	}
}

func TestOutbox_Run_DestinationError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	source := newMockSource()
	destination := newMockDestination()

	items := []Item{
		mockItem{entityId: "user1", id: "item1"},
	}
	source.setItems(items)

	destination.setError(true, "destination error")

	outbox := New(source, destination, Config{
		BatchSize:           10,
		MaxTries:            1, // Only 1 try to speed up test
		MaxSleepSec:         1,
		MaxConcurrentGroups: 5,
	}, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- outbox.Run(ctx)
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()

	<-errChan

	sendCalls := destination.getSendCalls()
	if len(sendCalls) == 0 {
		t.Error("expected some send calls, got 0")
	}

	markAsSentCalls := source.getMarkAsSentCalls()
	if len(markAsSentCalls) != 0 {
		t.Errorf("expected 0 mark as sent calls, got %d", len(markAsSentCalls))
	}
}

func TestOutbox_Run_Grouping(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	source := newMockSource()
	destination := newMockDestination()

	items := []Item{
		mockItem{entityId: "user1", id: "item1"},
		mockItem{entityId: "user1", id: "item2"},
		mockItem{entityId: "user2", id: "item3"},
		mockItem{entityId: "user2", id: "item4"},
		mockItem{entityId: "user3", id: "item5"},
	}
	source.setItems(items)

	outbox := New(source, destination, Config{
		BatchSize:           10,
		MaxTries:            3,
		MaxSleepSec:         1,
		MaxConcurrentGroups: 5,
	}, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- outbox.Run(ctx)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	<-errChan

	sendCalls := destination.getSendCalls()
	if len(sendCalls) != len(items) {
		t.Errorf("expected %d send calls, got %d", len(items), len(sendCalls))
	}

	markAsSentCalls := source.getMarkAsSentCalls()
	if len(markAsSentCalls) != len(items) {
		t.Errorf("expected %d mark as sent calls, got %d", len(items), len(markAsSentCalls))
	}
}

func TestOutbox_Run_BatchSize(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	source := newMockSource()
	destination := newMockDestination()

	items := make([]Item, 0, 15)
	for i := 0; i < 15; i++ {
		items = append(items, mockItem{entityId: "user1", id: fmt.Sprintf("item%d", i)})
	}
	source.setItems(items)

	outbox := New(source, destination, Config{
		BatchSize:           5, // Small batch size
		MaxTries:            3,
		MaxSleepSec:         1,
		MaxConcurrentGroups: 5,
	}, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- outbox.Run(ctx)
	}()

	time.Sleep(500 * time.Millisecond)
	cancel()

	<-errChan

	sendCalls := destination.getSendCalls()
	if len(sendCalls) != len(items) {
		t.Errorf("expected %d send calls, got %d", len(items), len(sendCalls))
	}
}

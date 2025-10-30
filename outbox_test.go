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
	sequence int64
}

func (m mockItem) GetEntityId() string { return m.entityId }
func (m mockItem) GetId() string       { return m.id }
func (m mockItem) GetSequence() int64  { return m.sequence }

type mockSource struct {
	items            []mockItem
	acknowledgeCalls []mockItem
	mu               sync.RWMutex
	shouldError      bool
	errorMsg         string
}

func newMockSource() *mockSource {
	return &mockSource{
		items:            make([]mockItem, 0),
		acknowledgeCalls: make([]mockItem, 0),
	}
}

func (m *mockSource) GetItems(ctx context.Context, batchSize int) ([]mockItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.shouldError {
		return nil, errors.New(m.errorMsg)
	}

	if len(m.items) == 0 {
		return []mockItem{}, nil
	}

	var result []mockItem
	if len(m.items) > batchSize {
		result = m.items[:batchSize]
		m.items = m.items[batchSize:]
	} else {
		result = m.items
		m.items = []mockItem{}
	}
	return result, nil
}

func (m *mockSource) Acknowledge(ctx context.Context, item mockItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.shouldError {
		return errors.New(m.errorMsg)
	}

	m.acknowledgeCalls = append(m.acknowledgeCalls, item)
	return nil
}

func (m *mockSource) setItems(items []mockItem) {
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

func (m *mockSource) getAcknowledgeCalls() []mockItem {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]mockItem(nil), m.acknowledgeCalls...)
}

type mockDestination struct {
	sendCalls     []mockItem
	sendManyCalls [][]mockItem
	mu            sync.RWMutex
	shouldError   bool
	errorMsg      string
}

func newMockDestination() *mockDestination {
	return &mockDestination{
		sendCalls:     make([]mockItem, 0),
		sendManyCalls: make([][]mockItem, 0),
	}
}

func (m *mockDestination) Send(ctx context.Context, item mockItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sendCalls = append(m.sendCalls, item)

	if m.shouldError {
		return errors.New(m.errorMsg)
	}

	return nil
}

func (m *mockDestination) SendMany(ctx context.Context, items []mockItem) error {
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

func (m *mockDestination) getSendCalls() []mockItem {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]mockItem(nil), m.sendCalls...)
}

func (m *mockDestination) getSendManyCalls() [][]mockItem {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([][]mockItem(nil), m.sendManyCalls...)
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
				SleepSec:            5,
				MaxConcurrentGroups: 30,
			},
		},
		{
			name: "custom values",
			config: Config{
				BatchSize:           10,
				SleepSec:            10,
				MaxConcurrentGroups: 5,
			},
			expected: Config{
				BatchSize:           10,
				SleepSec:            10,
				MaxConcurrentGroups: 5,
			},
		},
		{
			name: "negative values should be set to defaults",
			config: Config{
				BatchSize:           -1,
				SleepSec:            -1,
				MaxConcurrentGroups: -1,
			},
			expected: Config{
				BatchSize:           30,
				SleepSec:            5,
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
		SleepSec:            1, // Short sleep for testing
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

	items := []mockItem{
		{entityId: "user1", id: "item1"},
		{entityId: "user1", id: "item2"},
		{entityId: "user2", id: "item3"},
	}
	source.setItems(items)

	outbox := New(source, destination, Config{
		BatchSize:           10,
		SleepSec:            1,
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

	acknowledgeCalls := source.getAcknowledgeCalls()
	if len(acknowledgeCalls) != len(items) {
		t.Errorf("expected %d acknowledge calls, got %d", len(items), len(acknowledgeCalls))
	}
}

func TestOutbox_Run_ContextCancellation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	source := newMockSource()
	destination := newMockDestination()

	outbox := New(source, destination, Config{
		BatchSize:           10,
		SleepSec:            1,
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
		SleepSec:            1,
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

	items := []mockItem{
		{entityId: "user1", id: "item1"},
	}
	source.setItems(items)

	destination.setError(true, "destination error")

	outbox := New(source, destination, Config{
		BatchSize:           10,
		SleepSec:            1,
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

	acknowledgeCalls := source.getAcknowledgeCalls()
	if len(acknowledgeCalls) != 0 {
		t.Errorf("expected 0 acknowledge calls, got %d", len(acknowledgeCalls))
	}
}

func TestOutbox_Run_Grouping(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	source := newMockSource()
	destination := newMockDestination()

	items := []mockItem{
		{entityId: "user1", id: "item1"},
		{entityId: "user1", id: "item2"},
		{entityId: "user2", id: "item3"},
		{entityId: "user2", id: "item4"},
		{entityId: "user3", id: "item5"},
	}
	source.setItems(items)

	outbox := New(source, destination, Config{
		BatchSize:           10,
		SleepSec:            1,
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

	acknowledgeCalls := source.getAcknowledgeCalls()
	if len(acknowledgeCalls) != len(items) {
		t.Errorf("expected %d acknowledge calls, got %d", len(items), len(acknowledgeCalls))
	}
}

func TestOutbox_Run_BatchSize(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	source := newMockSource()
	destination := newMockDestination()

	items := make([]mockItem, 0, 15)
	for i := 0; i < 15; i++ {
		items = append(items, mockItem{entityId: "user1", id: fmt.Sprintf("item%d", i)})
	}
	source.setItems(items)

	outbox := New(source, destination, Config{
		BatchSize:           5, // Small batch size
		SleepSec:            1,
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

func TestOutbox_Run_SequenceOrdering(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	source := newMockSource()
	destination := newMockDestination()

	items := []mockItem{
		{entityId: "user1", id: "item1", sequence: 300},
		{entityId: "user1", id: "item2", sequence: 100},
		{entityId: "user1", id: "item3", sequence: 200},
		{entityId: "user2", id: "item4", sequence: 50},
		{entityId: "user2", id: "item5", sequence: 10},
	}
	source.setItems(items)

	outbox := New(source, destination, Config{
		BatchSize:           10,
		SleepSec:            1,
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

	var user1Items []mockItem
	var user2Items []mockItem
	for _, item := range sendCalls {
		if item.entityId == "user1" {
			user1Items = append(user1Items, item)
		} else if item.entityId == "user2" {
			user2Items = append(user2Items, item)
		}
	}

	if len(user1Items) != 3 {
		t.Errorf("expected 3 user1 items, got %d", len(user1Items))
	}
	if len(user2Items) != 2 {
		t.Errorf("expected 2 user2 items, got %d", len(user2Items))
	}

	if len(user1Items) == 3 {
		if user1Items[0].sequence != 100 {
			t.Errorf("user1 first item: expected sequence 100, got %d", user1Items[0].sequence)
		}
		if user1Items[1].sequence != 200 {
			t.Errorf("user1 second item: expected sequence 200, got %d", user1Items[1].sequence)
		}
		if user1Items[2].sequence != 300 {
			t.Errorf("user1 third item: expected sequence 300, got %d", user1Items[2].sequence)
		}
	}

	if len(user2Items) == 2 {
		if user2Items[0].sequence != 10 {
			t.Errorf("user2 first item: expected sequence 10, got %d", user2Items[0].sequence)
		}
		if user2Items[1].sequence != 50 {
			t.Errorf("user2 second item: expected sequence 50, got %d", user2Items[1].sequence)
		}
	}
}

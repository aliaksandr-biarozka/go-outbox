package outbox

import (
	"context"
	"log/slog"
	"os"
	"time"
)

func TestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
}

func TestConfig() Config {
	return Config{
		BatchSize:           5,
		MaxTries:            3,
		MaxSleepSec:         1,
		MaxConcurrentGroups: 2,
	}
}

func TestConfigWithShortSleep() Config {
	return Config{
		BatchSize:           5,
		MaxTries:            3,
		MaxSleepSec:         1,
		MaxConcurrentGroups: 2,
	}
}

func TestConfigWithLowRetries() Config {
	return Config{
		BatchSize:           5,
		MaxTries:            1,
		MaxSleepSec:         1,
		MaxConcurrentGroups: 2,
	}
}

func RunOutboxWithTimeout(outbox *Outbox, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- outbox.Run(ctx)
	}()

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func RunOutboxWithContext(outbox *Outbox, ctx context.Context) error {
	errChan := make(chan error, 1)
	go func() {
		errChan <- outbox.Run(ctx)
	}()

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

package gslb

import (
	"context"
	"fmt"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

// RetryableOperation represents a database operation that can be retried
type RetryableOperation func(context.Context) error

// ExecuteWithExponentialBackoff retries an operation with exponential backoff
// Uses 1s → 2s → 4s backoff strategy, up to maxRetries attempts
//
// Example:
//
//	err := ExecuteWithExponentialBackoff(ctx, func(ctx context.Context) error {
//	    _, err := collection.BulkWrite(ctx, operations)
//	    return err
//	}, 3, logger, "bulk shard acquisition")
func ExecuteWithExponentialBackoff(
	ctx context.Context,
	operation RetryableOperation,
	maxRetries int,
	logger *logger.Logger,
	operationName string,
) error {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		err := operation(ctx)
		if err == nil {
			if attempt > 0 {
				logger.Infof("%s succeeded on attempt %d/%d", operationName, attempt+1, maxRetries)
			}
			return nil
		}

		lastErr = err

		if attempt < maxRetries-1 {
			backoffDuration := time.Duration(1<<uint(attempt)) * time.Second
			logger.Warnf("%s failed (attempt %d/%d), retrying in %v: %v",
				operationName, attempt+1, maxRetries, backoffDuration, err)

			select {
			case <-time.After(backoffDuration):
				// Continue to next retry
			case <-ctx.Done():
				return fmt.Errorf("%s cancelled during backoff: %w", operationName, ctx.Err())
			}
		}
	}

	return fmt.Errorf("%s failed after %d attempts: %w", operationName, maxRetries, lastErr)
}

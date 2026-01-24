// Package registry provides client implementations for connecting to the registry service
// including controller registration and control-plane management.
package registry

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

// Shared gRPC connection configuration constants
// These are used by both control-plane and controller registry clients
const (
	DefaultConnectionTimeout = 30 * time.Second
	DefaultBackoffBaseDelay  = 1.0 * time.Second
	DefaultBackoffMultiplier = 1.6
	DefaultBackoffJitter     = 0.2
	DefaultBackoffMaxDelay   = 30 * time.Second
	DefaultKeepaliveTime     = 60 * time.Second
	DefaultKeepaliveTimeout  = 20 * time.Second
)

// RetryConfig configures retry behavior
type RetryConfig struct {
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Multiplier     float64
	MaxAttempts    int
}

// AggressiveRetryConfig returns a more aggressive retry configuration for critical operations
func AggressiveRetryConfig() RetryConfig {
	return RetryConfig{
		InitialBackoff: 500 * time.Millisecond, // Start faster
		MaxBackoff:     15 * time.Second,       // Cap at 15 seconds
		Multiplier:     1.3,                    // Gentler escalation
		MaxAttempts:    0,                      // Unlimited within context timeout
	}
}

// RetryWithBackoff executes a function with exponential backoff retry logic
// This eliminates duplicate retry code across registry clients
func RetryWithBackoff(ctx context.Context, operation string, config RetryConfig, logger *logger.Logger, fn func() error) error {
	backoff := config.InitialBackoff
	attempt := 1

	// Log initial attempt for important operations
	if attempt == 1 {
		logger.Debugf("Starting %s (attempt %d)", operation, attempt)
	}

	for {
		select {
		case <-ctx.Done():
			logger.Warnf("%s canceled after %d attempts due to context timeout: %v", operation, attempt-1, ctx.Err())
			return ctx.Err()
		default:
		}

		err := fn()
		if err == nil {
			if attempt > 1 {
				logger.Infof("Successfully completed %s after %d attempts", operation, attempt)
			} else {
				logger.Debugf("Successfully completed %s on first attempt", operation)
			}
			return nil
		}

		// Check if we've reached max attempts
		if config.MaxAttempts > 0 && attempt >= config.MaxAttempts {
			logger.Errorf("Failed to complete %s after %d attempts (max reached): %v", operation, config.MaxAttempts, err)
			return err
		}

		// Log retry attempt with backoff info
		logger.Warnf("Failed to %s (attempt %d): %v - retrying in %v", operation, attempt, err, backoff)

		// Wait before retry
		select {
		case <-ctx.Done():
			logger.Warnf("%s canceled during backoff after %d attempts: %v", operation, attempt, ctx.Err())
			return ctx.Err()
		case <-time.After(backoff):
		}

		// Exponential backoff with jitter to prevent thundering herd
		nextBackoff := time.Duration(float64(backoff) * config.Multiplier)
		if nextBackoff > config.MaxBackoff {
			nextBackoff = config.MaxBackoff
		}

		// Add small jitter (±10%) to prevent synchronized retries
		jitterFactor := float64(2*(time.Now().UnixNano()%2) - 1) // -1 or +1
		jitter := time.Duration(float64(nextBackoff) * 0.1 * jitterFactor)
		backoff = nextBackoff + jitter

		if backoff < config.InitialBackoff {
			backoff = config.InitialBackoff
		}
		if backoff > config.MaxBackoff {
			backoff = config.MaxBackoff
		}

		attempt++

		// Log progress for long-running retry sequences
		if attempt%5 == 0 {
			logger.Infof("Still retrying %s - attempt %d, current backoff: %v", operation, attempt, backoff)
		}
	}
}

// GetDefaultGRPCDialOptions returns the standard gRPC dial options
// used across all registry clients for consistency
func GetDefaultGRPCDialOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDisableServiceConfig(),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                DefaultKeepaliveTime,
			Timeout:             DefaultKeepaliveTimeout,
			PermitWithoutStream: false,
		}),
		grpc.WithDefaultCallOptions(
			grpc.WaitForReady(true),
		),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  DefaultBackoffBaseDelay,
				Multiplier: DefaultBackoffMultiplier,
				Jitter:     DefaultBackoffJitter,
				MaxDelay:   DefaultBackoffMaxDelay,
			},
		}),
	}
}

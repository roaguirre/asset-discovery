package fetchutil

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

type RetryOptions struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

func DefaultRetryOptions() RetryOptions {
	return RetryOptions{
		MaxAttempts:    3,
		InitialBackoff: 250 * time.Millisecond,
		MaxBackoff:     2 * time.Second,
	}
}

func DoRequest(ctx context.Context, client *http.Client, build func(context.Context) (*http.Request, error)) (*http.Response, error) {
	opts := DefaultRetryOptions()
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 1
	}

	backoff := opts.InitialBackoff
	var lastErr error

	for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
		req, err := build(ctx)
		if err != nil {
			return nil, err
		}

		resp, err := client.Do(req)
		if !shouldRetry(ctx, resp, err) {
			return resp, err
		}

		if resp != nil && resp.Body != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
		}

		if err != nil {
			lastErr = err
		}

		if attempt == opts.MaxAttempts {
			if err != nil {
				return nil, err
			}
			return resp, nil
		}

		wait := backoff
		if wait <= 0 {
			wait = 100 * time.Millisecond
		}
		if opts.MaxBackoff > 0 && wait > opts.MaxBackoff {
			wait = opts.MaxBackoff
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, ctx.Err()
		case <-timer.C:
		}

		if backoff <= 0 {
			backoff = 100 * time.Millisecond
		} else {
			backoff *= 2
			if opts.MaxBackoff > 0 && backoff > opts.MaxBackoff {
				backoff = opts.MaxBackoff
			}
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, context.Canceled
}

func shouldRetry(ctx context.Context, resp *http.Response, err error) bool {
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return false
		}
		if ctx != nil && ctx.Err() != nil {
			return false
		}

		var netErr net.Error
		if errors.As(err, &netErr) {
			return true
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return true
		}

		return true
	}

	if resp == nil {
		return false
	}

	switch resp.StatusCode {
	case http.StatusRequestTimeout,
		http.StatusTooEarly,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

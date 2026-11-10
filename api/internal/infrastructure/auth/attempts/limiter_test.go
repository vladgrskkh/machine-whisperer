package attempts_test

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/attempts"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

func TestFailedAttempts(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	limiter := attempts.New(attempts.Config{Now: func() time.Time { return now }})
	for range 5 {
		finish, retry, err := limiter.Begin(t.Context(), "alice@example.com")
		require.NoError(t, err)
		require.Zero(t, retry)
		finish(true)
		finish(false)
	}
	finish, retry, err := limiter.Begin(t.Context(), "alice@example.com")
	assert.Nil(t, finish)
	assert.ErrorIs(t, err, auth.ErrRateLimited)
	assert.Equal(t, time.Minute, retry)
	now = now.Add(time.Minute - time.Nanosecond)
	_, retry, err = limiter.Begin(t.Context(), "alice@example.com")
	assert.ErrorIs(t, err, auth.ErrRateLimited)
	assert.Equal(t, time.Nanosecond, retry)
	now = now.Add(time.Nanosecond)
	finish, _, err = limiter.Begin(t.Context(), "alice@example.com")
	require.NoError(t, err)
	finish(false)
}

func TestAttemptReleasePreservesFailures(t *testing.T) {
	t.Parallel()
	limiter := attempts.New(attempts.Config{Now: time.Now})
	for range 4 {
		finish, _, err := limiter.Begin(t.Context(), "a@example.com")
		require.NoError(t, err)
		finish(true)
	}
	for range 3 {
		finish, _, err := limiter.Begin(t.Context(), "a@example.com")
		require.NoError(t, err)
		finish(false)
	}
	finish, _, err := limiter.Begin(t.Context(), "a@example.com")
	require.NoError(t, err)
	finish(true)
	_, _, err = limiter.Begin(t.Context(), "a@example.com")
	assert.ErrorIs(t, err, auth.ErrRateLimited)
}

func TestAttemptsConcurrentReservations(t *testing.T) {
	t.Parallel()
	limiter := attempts.New(attempts.Config{Now: time.Now})
	var admitted atomic.Int32
	var ready, done sync.WaitGroup
	ready.Add(30)
	done.Add(30)
	release := make(chan struct{})
	for range 30 {
		go func() {
			defer done.Done()
			finish, _, err := limiter.Begin(t.Context(), "a@example.com")
			if err == nil {
				admitted.Add(1)
			}
			ready.Done()
			<-release
			if err == nil {
				finish(false)
				finish(true)
			}
		}()
	}
	ready.Wait()
	assert.EqualValues(t, 5, admitted.Load())
	close(release)
	done.Wait()
	for range 5 {
		finish, _, err := limiter.Begin(t.Context(), "a@example.com")
		require.NoError(t, err)
		finish(true)
	}
	_, _, err := limiter.Begin(t.Context(), "a@example.com")
	assert.ErrorIs(t, err, auth.ErrRateLimited)
}

func TestAttemptWindowsAndCapacity(t *testing.T) {
	t.Parallel()
	for _, delta := range []time.Duration{time.Minute, -time.Minute} {
		t.Run(delta.String(), func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
			limiter := attempts.New(attempts.Config{Now: func() time.Time { return now }, Capacity: 1})
			stale, _, err := limiter.Begin(t.Context(), "a@example.com")
			require.NoError(t, err)
			finish, retry, err := limiter.Begin(t.Context(), "b@example.com")
			assert.Nil(t, finish)
			assert.ErrorIs(t, err, auth.ErrRateLimited)
			assert.Positive(t, retry)
			now = now.Add(delta)
			current, _, err := limiter.Begin(t.Context(), "a@example.com")
			require.NoError(t, err)
			stale(true)
			stale(false)
			for range 4 {
				finish, _, err = limiter.Begin(t.Context(), "a@example.com")
				require.NoError(t, err)
				finish(true)
			}
			current(false)
			finish, _, err = limiter.Begin(t.Context(), "a@example.com")
			require.NoError(t, err)
			finish(true)
			_, _, err = limiter.Begin(t.Context(), "a@example.com")
			assert.ErrorIs(t, err, auth.ErrRateLimited)
			now = now.Add(time.Minute)
			finish, _, err = limiter.Begin(t.Context(), "b@example.com")
			require.NoError(t, err)
			finish(false)
			// Successful identities release capacity immediately.
			finish, _, err = limiter.Begin(t.Context(), "c@example.com")
			require.NoError(t, err)
			finish(false)
		})
	}
}

func TestAttemptsCancellation(t *testing.T) {
	t.Parallel()
	limiter := attempts.New(attempts.Config{Now: time.Now, Capacity: 1})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	finish, _, err := limiter.Begin(ctx, "a@example.com")
	assert.Nil(t, finish)
	assert.ErrorIs(t, err, context.Canceled)
	finish, _, err = limiter.Begin(t.Context(), "b@example.com")
	require.NoError(t, err)
	finish(false)
}

func TestAttemptCompletionConcurrentExactlyOnce(t *testing.T) {
	t.Parallel()
	limiter := attempts.New(attempts.Config{Now: time.Now})
	finish, _, err := limiter.Begin(t.Context(), "a@example.com")
	require.NoError(t, err)
	var done sync.WaitGroup
	for range 30 {
		done.Go(func() { finish(true) })
	}
	done.Wait()
	for range 4 {
		next, _, beginErr := limiter.Begin(t.Context(), "a@example.com")
		require.NoError(t, beginErr)
		next(true)
	}
	_, _, err = limiter.Begin(t.Context(), "a@example.com")
	assert.ErrorIs(t, err, auth.ErrRateLimited)
}

func TestAttemptsDefaultCapacityFailsClosed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	limiter := attempts.New(attempts.Config{Now: func() time.Time { return now }})
	for i := range 10_000 {
		finish, _, err := limiter.Begin(t.Context(), strconv.Itoa(i))
		require.NoError(t, err)
		finish(true)
	}
	for range 4 {
		finish, _, err := limiter.Begin(t.Context(), "0")
		require.NoError(t, err)
		finish(true)
	}
	finish, retry, err := limiter.Begin(t.Context(), "new")
	assert.Nil(t, finish)
	assert.ErrorIs(t, err, auth.ErrRateLimited)
	assert.Equal(t, time.Minute, retry)
	_, _, err = limiter.Begin(t.Context(), "0")
	assert.ErrorIs(t, err, auth.ErrRateLimited)
	now = now.Add(time.Minute)
	finish, _, err = limiter.Begin(t.Context(), "new")
	require.NoError(t, err)
	finish(false)
}

func TestAttemptsConfiguredLimitAndWindow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	limiter := attempts.New(
		attempts.Config{Now: func() time.Time { return now }, Limit: 2, Window: 10 * time.Second, Capacity: 1},
	)
	for range 2 {
		finish, _, err := limiter.Begin(t.Context(), "a@example.com")
		require.NoError(t, err)
		finish(true)
	}
	finish, retry, err := limiter.Begin(t.Context(), "a@example.com")
	assert.Nil(t, finish)
	assert.ErrorIs(t, err, auth.ErrRateLimited)
	assert.Equal(t, 10*time.Second, retry)
	// A full identity table uses the same configured retry window.
	finish, retry, err = limiter.Begin(t.Context(), "b@example.com")
	assert.Nil(t, finish)
	assert.ErrorIs(t, err, auth.ErrRateLimited)
	assert.Equal(t, 10*time.Second, retry)
	now = now.Add(10*time.Second - time.Nanosecond)
	_, retry, err = limiter.Begin(t.Context(), "a@example.com")
	assert.ErrorIs(t, err, auth.ErrRateLimited)
	assert.Equal(t, time.Nanosecond, retry)
	now = now.Add(time.Nanosecond)
	finish, _, err = limiter.Begin(t.Context(), "a@example.com")
	require.NoError(t, err)
	finish(false)
}

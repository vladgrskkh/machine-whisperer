package main

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunReturnsListenError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	assert.Error(t, run(context.Background(), listener.Addr().String(), http.NotFoundHandler(), func(context.Context) {
		t.Error("worker started without an HTTP listener")
	}))
}

func TestRunShutsDownOnCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	workerStarted := make(chan struct{})
	workerCanceled := make(chan struct{})
	workerCanStop := make(chan struct{})
	stopWorker := sync.OnceFunc(func() { close(workerCanStop) })
	t.Cleanup(stopWorker)
	runWorkers := func(ctx context.Context) {
		close(workerStarted)
		<-ctx.Done()
		close(workerCanceled)
		<-workerCanStop
	}
	result := make(chan error, 1)
	go func() { result <- run(ctx, address, http.NotFoundHandler(), runWorkers) }()
	client := &http.Client{Timeout: time.Second}
	require.Eventually(t, func() bool {
		response, err := client.Get("http://" + address + "/unknown")
		if err != nil {
			return false
		}
		_ = response.Body.Close()
		return response.StatusCode == http.StatusNotFound
	}, 2*time.Second, 10*time.Millisecond)
	select {
	case <-workerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start")
	}
	cancel()
	select {
	case <-workerCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not receive cancellation")
	}
	assert.Never(t, func() bool { return len(result) > 0 }, 50*time.Millisecond, 5*time.Millisecond)
	stopWorker()
	select {
	case err := <-result:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}
}

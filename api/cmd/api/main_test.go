package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunReturnsListenError(t *testing.T) {
	t.Setenv("API_TLS_CERT_FILE", "")
	t.Setenv("API_TLS_KEY_FILE", "")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	assert.Error(t, run(context.Background(), listener.Addr().String(), http.NotFoundHandler(), func(context.Context) {
		t.Error("worker started without an HTTP listener")
	}))
}

func TestRunShutsDownOnCancellation(t *testing.T) {
	t.Setenv("API_TLS_CERT_FILE", "")
	t.Setenv("API_TLS_KEY_FILE", "")
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

func TestRunRejectsIncompleteTLS(t *testing.T) {
	for _, variable := range []string{"API_TLS_CERT_FILE", "API_TLS_KEY_FILE"} {
		t.Run(variable, func(t *testing.T) {
			t.Setenv("API_TLS_CERT_FILE", "")
			t.Setenv("API_TLS_KEY_FILE", "")
			t.Setenv(variable, "private-tls-path")
			ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
			defer cancel()
			err := run(ctx, "127.0.0.1:0", http.NotFoundHandler(), func(context.Context) {
				t.Error("worker started with incomplete TLS configuration")
			})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private-tls-path")
		})
	}
}

func TestRunServesHTTPS(t *testing.T) {
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(fixture.Close)
	certificate := fixture.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	require.NoError(t, err)
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: certificate.Certificate[0],
	}), 0o600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{
		Type: "PRIVATE KEY", Bytes: key,
	}), 0o600))
	t.Setenv("API_TLS_CERT_FILE", certPath)
	t.Setenv("API_TLS_KEY_FILE", keyPath)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- run(ctx, address, http.NotFoundHandler(), func(context.Context) {}) }()
	t.Cleanup(func() {
		cancel()
		select {
		case runErr := <-result:
			assert.NoError(t, runErr)
		case <-time.After(2 * time.Second):
			t.Error("TLS server did not stop")
		}
	})

	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: time.Second}
	require.Eventually(t, func() bool {
		response, requestErr := client.Get("https://" + address)
		if requestErr != nil {
			return false
		}
		_ = response.Body.Close()
		return response.StatusCode == http.StatusNotFound
	}, 2*time.Second, 10*time.Millisecond)
}

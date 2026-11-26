package tracing

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/url"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// MobileForwarder returns a lazy client for the same private OTLP receiver as the API.
func MobileForwarder() (func(context.Context, *coltrace.ExportTraceServiceRequest) error, func() error, error) {
	endpoint, err := url.Parse(otlpEndpoint())
	if err != nil || endpoint == nil || endpoint.Hostname() == "" ||
		(endpoint.Scheme != "http" && endpoint.Scheme != httpsScheme) {
		return nil, nil, errors.New("invalid OTLP origin for mobile traces")
	}
	host := endpoint.Host
	transport := insecure.NewCredentials()
	port := "80"
	if endpoint.Scheme == httpsScheme {
		transport = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
		port = "443"
	}
	if endpoint.Port() == "" {
		host = net.JoinHostPort(endpoint.Hostname(), port)
	}
	conn, err := grpc.NewClient(host, grpc.WithTransportCredentials(transport))
	if err != nil {
		return nil, nil, errors.New("cannot create mobile trace forwarder")
	}
	client := coltrace.NewTraceServiceClient(conn)
	return func(ctx context.Context, spans *coltrace.ExportTraceServiceRequest) error {
		_, exportErr := client.Export(ctx, spans)
		return exportErr
	}, conn.Close, nil
}

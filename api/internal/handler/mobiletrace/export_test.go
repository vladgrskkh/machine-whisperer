package mobiletrace

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestSanitizeMobileTrace(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	h := &Handler{key: bytes.Repeat([]byte{1}, 32), now: func() time.Time { return now }}
	serverTrace, err := trace.TraceIDFromHex("12345678123456781234567812345678")
	require.NoError(t, err)
	serverSpan, err := trace.SpanIDFromHex("1234567812345678")
	require.NoError(t, err)
	ctx := trace.NewSpanContext(
		trace.SpanContextConfig{TraceID: serverTrace, SpanID: serverSpan, TraceFlags: trace.FlagsSampled},
	)
	traceID := bytes.Repeat([]byte{2}, 16)
	rootID := bytes.Repeat([]byte{3}, 8)
	timestamp := uint64(now.UnixNano())
	root := &tracepb.Span{
		Name: actionName, Kind: tracepb.Span_SPAN_KIND_INTERNAL, TraceId: traceID, SpanId: rootID,
		StartTimeUnixNano: timestamp - uint64(time.Millisecond), EndTimeUnixNano: timestamp,
		TraceState: "user=private-trace-state",
		Events:     []*tracepb.Span_Event{{Name: "private-body"}},
	}
	attempt := &tracepb.Span{
		Name: requestName, Kind: tracepb.Span_SPAN_KIND_CLIENT, TraceId: traceID, SpanId: bytes.Repeat([]byte{4}, 8),
		ParentSpanId: rootID, StartTimeUnixNano: timestamp - uint64(time.Microsecond), EndTimeUnixNano: timestamp,
		Links: []*tracepb.Span_Link{
			{TraceId: serverTrace[:], SpanId: serverSpan[:], TraceState: "vendor=private-link"},
			{TraceId: bytes.Repeat([]byte{6}, 16), SpanId: bytes.Repeat([]byte{7}, 8)},
		},
		Attributes: []*common.KeyValue{
			textAttribute("http.route", "private-path"),
			textAttribute("authorization", "private-token"),
			{
				Key:   "http.response.status_code",
				Value: &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: 200}},
			},
		},
		Status: &tracepb.Status{Message: "private-status", Code: tracepb.Status_STATUS_CODE_OK},
	}
	input := &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{
		{
			Resource: &resource.Resource{
				Attributes: []*common.KeyValue{
					textAttribute("service.name", "api"),
					textAttribute("service.version", "1.0.0"),
				},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{root, attempt}}},
		},
	}}
	receipt := h.makeReceipt("alice", ctx)

	output, err := h.sanitize(input, "alice", receipt)
	require.NoError(t, err)
	records := output.GetResourceSpans()[0].GetScopeSpans()[0].GetSpans()
	require.Len(t, records, 2)
	assert.NotEqual(t, traceID, records[0].GetTraceId())
	assert.Equal(t, records[0].GetTraceId(), records[1].GetTraceId())
	assert.Equal(t, rootID, records[1].GetParentSpanId())
	require.Len(t, records[1].GetLinks(), 1)
	assert.Equal(t, serverTrace.String(), hex.EncodeToString(records[1].GetLinks()[0].GetTraceId()))
	assert.Equal(t, serverSpan.String(), hex.EncodeToString(records[1].GetLinks()[0].GetSpanId()))
	assert.Equal(t, "mobile", output.GetResourceSpans()[0].GetResource().GetAttributes()[0].GetValue().GetStringValue())
	raw, err := proto.Marshal(output)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "private-")
	assert.NotContains(t, string(raw), receipt)

	other, err := h.sanitize(input, "bob", receipt)
	require.NoError(t, err)
	otherRecords := other.GetResourceSpans()[0].GetScopeSpans()[0].GetSpans()
	assert.Empty(t, otherRecords[1].GetLinks())
	assert.NotEqual(t, records[0].GetTraceId(), otherRecords[0].GetTraceId())
}

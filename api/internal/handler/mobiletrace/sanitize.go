package mobiletrace

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

const (
	actionName  = "machine.history.load"
	route       = "/v1/machines/{machine_id}/telemetry"
	requestName = "GET " + route
	maxSpans    = 16
	maxAge      = 10 * time.Minute
	maxFuture   = time.Minute
	maxDuration = time.Minute
)

var (
	errInvalidSpan = errors.New("invalid mobile span")
	validVersion   = regexp.MustCompile(`^[0-9]{1,8}(\.[0-9]{1,8}){1,3}$`)
)

func (h *Handler) sanitize(
	input *coltrace.ExportTraceServiceRequest, user, receipts string,
) (*coltrace.ExportTraceServiceRequest, error) {
	allowed := make(map[traceLink]struct{})
	for receipt := range strings.SplitSeq(receipts, ",") {
		if link, ok := h.readReceipt(user, receipt); ok {
			allowed[link] = struct{}{}
		}
	}
	if len(input.GetResourceSpans()) != 1 {
		return nil, errInvalidSpan
	}
	envelope := input.GetResourceSpans()[0]
	if len(envelope.GetScopeSpans()) != 1 {
		return nil, errInvalidSpan
	}
	incoming := envelope.GetScopeSpans()[0].GetSpans()
	if len(incoming) == 0 || len(incoming) > maxSpans {
		return nil, errInvalidSpan
	}

	meta := []*common.KeyValue{textAttribute("service.name", "mobile"),
		textAttribute("service.namespace", "machine-whisperer")}
	for _, attr := range envelope.GetResource().GetAttributes() {
		value := attr.GetValue().GetStringValue()
		if attr.GetKey() == "service.version" && validVersion.MatchString(value) {
			meta = append(meta, textAttribute("service.version", value))
			break
		}
	}

	spans := make([]*tracepb.Span, 0, len(incoming))
	for _, in := range incoming {
		span, err := h.sanitizeSpan(in, user, allowed)
		if err != nil {
			return nil, err
		}
		spans = append(spans, span)
	}
	return &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resource.Resource{Attributes: meta},
		ScopeSpans: []*tracepb.ScopeSpans{{
			Scope: &common.InstrumentationScope{Name: "machine-whisperer.mobile"}, Spans: spans,
		}},
	}}}, nil
}

func (h *Handler) sanitizeSpan(in *tracepb.Span, user string, allowed map[traceLink]struct{}) (*tracepb.Span, error) {
	if !validIDs(in.GetTraceId(), in.GetSpanId()) {
		return nil, errInvalidSpan
	}
	start, end := in.GetStartTimeUnixNano(), in.GetEndTimeUnixNano()
	now := h.now()
	if start < uint64(now.Add(-maxAge).UnixNano()) || end > uint64(now.Add(maxFuture).UnixNano()) ||
		end < start || end-start > uint64(maxDuration) {
		return nil, errInvalidSpan
	}

	span := &tracepb.Span{
		Name: in.GetName(), Kind: in.GetKind(), TraceId: h.digest("trace-id/v1", user, in.GetTraceId())[:16],
		SpanId: in.GetSpanId(), ParentSpanId: in.GetParentSpanId(), StartTimeUnixNano: start, EndTimeUnixNano: end,
		Flags: uint32(trace.FlagsSampled),
	}
	switch {
	case span.GetName() == actionName && span.GetKind() == tracepb.Span_SPAN_KIND_INTERNAL &&
		len(span.GetParentSpanId()) == 0:
	case span.GetName() == requestName && span.GetKind() == tracepb.Span_SPAN_KIND_CLIENT &&
		validIDs(in.GetTraceId(), span.GetParentSpanId()):
		span.Attributes = []*common.KeyValue{
			textAttribute("http.request.method", "GET"),
			textAttribute("http.route", route),
		}
		span.Links = verifiedLink(in.GetLinks(), allowed)
	default:
		return nil, errInvalidSpan
	}

	if in.GetStatus().GetCode() == tracepb.Status_STATUS_CODE_ERROR {
		span.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR}
	}
	for _, attr := range in.GetAttributes() {
		switch attr.GetKey() {
		case "http.response.status_code":
			if status := attr.GetValue().
				GetIntValue(); span.GetName() == requestName && status >= 100 &&
				status <= 599 {
				span.Attributes = append(span.Attributes, &common.KeyValue{
					Key: attr.GetKey(), Value: &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: status}},
				})
			}
		case "error.type":
			switch value := attr.GetValue().GetStringValue(); value {
			case "network_error", "timeout", "cancelled", "http_error", "invalid_response":
				span.Attributes = append(span.Attributes, textAttribute(attr.GetKey(), value))
			}
		}
	}
	return span, nil
}

func validIDs(traceID, spanID []byte) bool {
	return len(traceID) == 16 && len(spanID) == 8 && trace.TraceID(traceID).IsValid() && trace.SpanID(spanID).IsValid()
}

func verifiedLink(links []*tracepb.Span_Link, allowed map[traceLink]struct{}) []*tracepb.Span_Link {
	for _, in := range links {
		if !validIDs(in.GetTraceId(), in.GetSpanId()) {
			continue
		}
		key := traceLink{traceID: trace.TraceID(in.GetTraceId()), spanID: trace.SpanID(in.GetSpanId())}
		if _, ok := allowed[key]; ok {
			return []*tracepb.Span_Link{
				{TraceId: in.GetTraceId(), SpanId: in.GetSpanId(), Flags: uint32(trace.FlagsSampled)},
			}
		}
	}
	return nil
}

func textAttribute(key, value string) *common.KeyValue {
	return &common.KeyValue{Key: key, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: value}}}
}

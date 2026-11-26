package mobiletrace

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"time"

	"go.opentelemetry.io/otel/trace"
)

const (
	receiptBodyBytes = 32
	receiptLifetime  = 2 * time.Minute
)

type traceLink struct {
	traceID trace.TraceID
	spanID  trace.SpanID
}

func (h *Handler) makeReceipt(user string, span trace.SpanContext) string {
	expires := h.now().Add(receiptLifetime).Unix()
	if expires <= 0 {
		return ""
	}
	var material [receiptBodyBytes + sha256.Size]byte
	binary.BigEndian.PutUint64(material[:8], uint64(expires))
	traceID, spanID := span.TraceID(), span.SpanID()
	copy(material[8:24], traceID[:])
	copy(material[24:32], spanID[:])
	copy(material[receiptBodyBytes:], h.digest("receipt/v1", user, material[:receiptBodyBytes]))
	return base64.RawURLEncoding.EncodeToString(material[:])
}

func (h *Handler) readReceipt(user, receipt string) (traceLink, bool) {
	var link traceLink
	value, err := base64.RawURLEncoding.DecodeString(receipt)
	if err != nil || len(value) != receiptBodyBytes+sha256.Size {
		return link, false
	}
	body := value[:receiptBodyBytes]
	now := h.now().Unix()
	if now < 0 || !hmac.Equal(value[receiptBodyBytes:], h.digest("receipt/v1", user, body)) ||
		binary.BigEndian.Uint64(body[:8]) <= uint64(now) {
		return link, false
	}
	copy(link.traceID[:], body[8:24])
	copy(link.spanID[:], body[24:32])
	return link, link.traceID.IsValid() && link.spanID.IsValid()
}

func (h *Handler) digest(purpose, user string, body []byte) []byte {
	mac := hmac.New(sha256.New, h.key)
	_, _ = mac.Write([]byte(purpose + "\x00" + user + "\x00"))
	_, _ = mac.Write(body)
	return mac.Sum(nil)
}

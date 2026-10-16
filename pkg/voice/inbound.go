package voice

import (
	"encoding/json"
	"log/slog"
	"strings"
)

// Reasons of a normalized EventFailed. They are generic on purpose: no
// upstream message, body or ID crosses to the client or the logs.
const (
	FailProviderError   = "provider_error"
	FailInboundOverflow = "inbound_overflow"
)

// fatalErrorCodes end the call whatever else the error carries. The Live
// delegation guide documents responses_validation_failed as arriving after
// session.started with the session left open: for moa that means a voice
// with no usable backend or tools, so the call ends. The rest are session-
// level credential, entitlement or quota failures.
var fatalErrorCodes = map[string]bool{
	"responses_validation_failed": true,
	"session_expired":             true,
	"invalid_api_key":             true,
	"insufficient_quota":          true,
	"unauthorized":                true,
	"forbidden":                   true,
	"access_denied":               true,
	"permission_denied":           true,
}

// fatalErrorTypes are session-level error types.
var fatalErrorTypes = map[string]bool{
	"authentication_error": true,
	"permission_error":     true,
	"server_error":         true,
}

// providerErrorFatal classifies an `error` event received after open and
// returns the safe diagnostic of a fatal one.
//
// Rule, from the documented error envelope {type, code, client_event_id}:
//  1. a fatal code or type ends the call (checked first: a validation failure
//     answering one command is still fatal);
//  2. otherwise an error tied to one rejected client command is not fatal:
//     it carries error.client_event_id, or has type invalid_request_error
//     (e.g. cancelling a response that is no longer active);
//  3. anything else, including an envelope we cannot read, is fatal: an
//     unclassified session error must not leave a call that looks alive.
func providerErrorFatal(raw []byte) (bool, *Diagnostic) {
	var e struct {
		Error struct {
			Type          string `json:"type"`
			Code          string `json:"code"`
			ClientEventID string `json:"client_event_id"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) != nil {
		slog.Warn("voice provider error", "fatal", true)
		return true, &Diagnostic{Cause: FailProviderError}
	}
	typ, code := strings.ToLower(e.Error.Type), strings.ToLower(e.Error.Code)
	fatal := true
	switch {
	case fatalErrorCodes[code] || fatalErrorTypes[typ]:
	case e.Error.ClientEventID != "" || typ == "invalid_request_error":
		fatal = false
	}
	d := &Diagnostic{Cause: FailProviderError, ErrorCode: knownCode(code), ErrorType: knownType(typ)}
	slog.Warn("voice provider error", "type", d.ErrorType, "code", d.ErrorCode, "fatal", fatal)
	if !fatal {
		return false, nil
	}
	return true, d
}

// knownCode and knownType keep only the provider enums documented above:
// an enum-like string we do not know could still carry anything, so it is
// reported as the literal "unknown".
func knownCode(s string) string {
	if s == "" || fatalErrorCodes[s] {
		return s
	}
	return "unknown"
}

func knownType(s string) string {
	if s == "" || fatalErrorTypes[s] || s == "invalid_request_error" {
		return s
	}
	return "unknown"
}

// inboundAudio bounds the provider PCM bytes waiting in a buffered channel with a
// single producer, without asking the consumer to report: what left the
// channel is inferred from its length, oldest first. The producer calls
// admit for every value it is about to send, in send order.
type inboundAudio struct {
	sizes []int // PCM bytes of each value that may still be queued, FIFO
	bytes int
}

// admit reports whether pcm more bytes fit given queued values still in the
// channel; on true the value is counted and must be sent.
func (q *inboundAudio) admit(queued, pcm int) bool {
	for len(q.sizes) > queued {
		q.bytes -= q.sizes[0]
		q.sizes = q.sizes[1:]
	}
	if q.bytes+pcm > MaxPendingOutputAudio {
		return false
	}
	q.sizes = append(q.sizes, pcm)
	q.bytes += pcm
	return true
}

// overflow describes the rejection just reported by admit.
func (q *inboundAudio) overflow(pcm int) *Diagnostic {
	chunk, queued, limit := pcm, q.bytes, MaxPendingOutputAudio
	return &Diagnostic{Cause: FailInboundOverflow, ChunkBytes: &chunk, QueuedBytes: &queued, LimitBytes: &limit}
}

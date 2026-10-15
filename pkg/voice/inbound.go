package voice

import (
	"encoding/json"
	"log/slog"
	"regexp"
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

// providerErrorFatal classifies an `error` event received after open.
//
// Rule, from the documented error envelope {type, code, client_event_id}:
//  1. a fatal code or type ends the call (checked first: a validation failure
//     answering one command is still fatal);
//  2. otherwise an error tied to one rejected client command is not fatal:
//     it carries error.client_event_id, or has type invalid_request_error
//     (e.g. cancelling a response that is no longer active);
//  3. anything else, including an envelope we cannot read, is fatal: an
//     unclassified session error must not leave a call that looks alive.
func providerErrorFatal(raw []byte) bool {
	var e struct {
		Error struct {
			Type          string `json:"type"`
			Code          string `json:"code"`
			ClientEventID string `json:"client_event_id"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return true
	}
	typ, code := strings.ToLower(e.Error.Type), strings.ToLower(e.Error.Code)
	fatal := true
	switch {
	case fatalErrorCodes[code] || fatalErrorTypes[typ]:
	case e.Error.ClientEventID != "" || typ == "invalid_request_error":
		fatal = false
	}
	slog.Warn("voice provider error", "type", safeToken(typ), "code", safeToken(code), "fatal", fatal)
	return fatal
}

var tokenRe = regexp.MustCompile(`^[a-z0-9_.]{1,64}$`)

// safeToken keeps an enum-like provider token for the logs and drops
// anything that could be free text.
func safeToken(s string) string {
	if tokenRe.MatchString(s) {
		return s
	}
	return ""
}

// inboundAudio bounds the PCM bytes waiting in a buffered channel with a
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
	if q.bytes+pcm > MaxPendingAudio {
		return false
	}
	q.sizes = append(q.sizes, pcm)
	q.bytes += pcm
	return true
}

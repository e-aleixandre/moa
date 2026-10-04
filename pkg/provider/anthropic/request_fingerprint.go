package anthropic

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// fingerprintRequestBody describes the exact bytes about to be sent without
// retaining any of their content: SHA-256 digests and cache-marker positions
// only. body is never re-marshaled for transport; it is decoded once, with
// numbers kept verbatim, solely to derive the section hashes.
//
// Block hashes ignore the block's own top-level cache_control so a marker that
// moves between requests does not change content identity; markers are
// reported separately. cache_control nested deeper (tool schemas, tool_result
// content) is content and stays in the hash.
func fingerprintRequestBody(body []byte, builtAt time.Time) (core.RequestFingerprint, error) {
	fp := core.RequestFingerprint{BuiltAt: builtAt, BodySHA256: sumHex(body)}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var top map[string]any
	if err := dec.Decode(&top); err != nil {
		return core.RequestFingerprint{}, errors.New("decode")
	}
	if _, err := dec.Token(); err != io.EOF {
		return core.RequestFingerprint{}, errors.New("trailing data")
	}

	options := make(map[string]any, len(top))
	for k, v := range top {
		switch k {
		case "tools", "system", "messages":
		default:
			options[k] = v
		}
	}
	var err error
	if fp.OptionsSHA256, err = hashValue(options); err != nil {
		return core.RequestFingerprint{}, err
	}

	roll := sha256.New()
	add := func(section string, msg, blk int, frames ...[]byte) {
		for _, f := range frames {
			writeFrame(roll, f)
		}
		fp.Prefixes = append(fp.Prefixes, core.RequestPrefixHash{Section: section, Message: msg, Block: blk, SHA256: hex.EncodeToString(roll.Sum(nil))})
	}

	// section hashes the ordered, marker-free blocks of tools or system; the
	// same per-block digests feed the rolling prefix.
	section := func(name string, blocks []any) (string, error) {
		h := sha256.New()
		writeFrame(h, []byte(name))
		for i, raw := range blocks {
			blk, marker := stripMarker(raw)
			enc, err := canonical(blk)
			if err != nil {
				return "", err
			}
			writeFrame(h, enc)
			add(name, -1, i, []byte(name), enc)
			if bp, ok := breakpointOf(name, -1, i, marker); ok {
				fp.Breakpoints = append(fp.Breakpoints, bp)
			}
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}

	if tools, ok := top["tools"].([]any); ok && len(tools) > 0 {
		if fp.ToolsSHA256, err = section("tools", tools); err != nil {
			return core.RequestFingerprint{}, err
		}
	}
	switch sys := top["system"].(type) {
	case []any:
		if len(sys) > 0 {
			if fp.SystemSHA256, err = section("system", sys); err != nil {
				return core.RequestFingerprint{}, err
			}
		}
	case string:
		if sys != "" {
			if fp.SystemSHA256, err = section("system", []any{sys}); err != nil {
				return core.RequestFingerprint{}, err
			}
		}
	}

	msgs, _ := top["messages"].([]any)
	for mi, rawMsg := range msgs {
		m, ok := rawMsg.(map[string]any)
		if !ok {
			return core.RequestFingerprint{}, errors.New("message shape")
		}
		envelope := make(map[string]any, len(m))
		for k, v := range m {
			if k != "content" {
				envelope[k] = v
			}
		}
		env, err := canonical(envelope)
		if err != nil {
			return core.RequestFingerprint{}, err
		}
		writeFrame(roll, []byte("message"))
		writeFrame(roll, env)

		var blocks []any
		switch c := m["content"].(type) {
		case []any:
			blocks = c
		case nil:
		default:
			blocks = []any{c}
		}
		for bi, raw := range blocks {
			blk, marker := stripMarker(raw)
			enc, err := canonical(blk)
			if err != nil {
				return core.RequestFingerprint{}, err
			}
			add("messages", mi, bi, []byte("block"), enc)
			if bp, ok := breakpointOf("messages", mi, bi, marker); ok {
				fp.Breakpoints = append(fp.Breakpoints, bp)
			}
		}
	}
	return fp, nil
}

// stripMarker returns block without its own top-level cache_control, plus that
// marker. Only maps carry markers; the input is not mutated.
func stripMarker(block any) (any, any) {
	m, ok := block.(map[string]any)
	if !ok {
		return block, nil
	}
	marker, has := m["cache_control"]
	if !has {
		return block, nil
	}
	out := make(map[string]any, len(m)-1)
	for k, v := range m {
		if k != "cache_control" {
			out[k] = v
		}
	}
	return out, marker
}

// breakpointOf reduces a marker to position and TTL; an unrecognized TTL
// collapses to "other" so no arbitrary request value reaches the audit.
func breakpointOf(section string, msg, blk int, marker any) (core.RequestBreakpoint, bool) {
	if marker == nil {
		return core.RequestBreakpoint{}, false
	}
	bp := core.RequestBreakpoint{Section: section, Message: msg, Block: blk, TTL: "5m"}
	if cc, ok := marker.(map[string]any); ok {
		if ttl, ok := cc["ttl"].(string); ok {
			bp.TTLExplicit = true
			switch ttl {
			case "5m", "1h":
				bp.TTL = ttl
			default:
				bp.TTL = "other"
			}
		}
	}
	return bp, true
}

func canonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, errors.New("encode")
	}
	return buf.Bytes(), nil
}

func hashValue(v any) (string, error) {
	enc, err := canonical(v)
	if err != nil {
		return "", err
	}
	return sumHex(enc), nil
}

func sumHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// writeFrame length-prefixes each item so adjacent items cannot be confused,
// and writes nothing about how many items follow.
func writeFrame(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	h.Write(n[:])
	h.Write(b)
}

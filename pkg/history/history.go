// Package history provides the read-only `history` tool: the agent searches
// the original messages of its own session — including those already
// summarised by a compaction, cut by a fresh start or elided by a trim — reads
// one by ID and looks at its neighbours.
//
// It reads; it never changes the model's context, the compaction or the
// session. What it returns is bounded so that a 50 MB transcript cannot be
// dumped into one tool result.
package history

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/core"
)

// Output and argument bounds.
const (
	MaxOutputBytes = 16 << 10
	MaxQueryBytes  = 1 << 10
	readPageBytes  = 12 << 10
	snippetBytes   = 240
	nameBytes      = 64
	foldChunk      = 64 << 10
	windowItemMax  = 600
	defaultLimit   = 20
	maxLimit       = 50
	defaultWindow  = 5
	maxWindow      = 20
)

const toolName = "history"

// Visitor calls fn with every message of the session transcript in
// chronological order (the current branch, markers included). fn must not
// retain the pointer.
type Visitor func(fn func(*core.AgentMessage)) error

// NewTool builds the history tool over a session's transcript.
func NewTool(visit Visitor) core.Tool {
	return core.Tool{
		Name:  toolName,
		Label: "History",
		Description: "Search the original messages of this session, including those no longer in your context " +
			"(before a compaction or a fresh start, or trimmed tool outputs). " +
			"search finds messages containing every word of query (case- and accent-insensitive; put a phrase in double quotes), oldest first, " +
			"with each message's id, author and a snippet; role narrows to user, assistant, tool or ask_user (the answers to ask_user, with who answered). " +
			"read returns one message by id; around shows the messages before and after it. " +
			"Use it to check what was actually said or decided before relying on a summary. Zero matches does not prove absence: try other words.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["search", "read", "around"]},
				"query": {"type": "string", "description": "search: words that must all appear in the message; \"quoted phrase\" for an exact phrase. Optional when role is set."},
				"role": {"type": "string", "enum": ["all", "user", "assistant", "tool", "ask_user"], "description": "search: only messages of this kind (default all)"},
				"after": {"type": "string", "description": "search: only messages after this id (to continue a long result)"},
				"limit": {"type": "integer", "description": "search: max matches to show (1-50, default 20)"},
				"id": {"type": "string", "description": "read/around: message id"},
				"offset": {"type": "integer", "description": "read: byte offset to continue a long message"},
				"before": {"type": "integer", "description": "around: messages before (0-20, default 5)"},
				"after_count": {"type": "integer", "description": "around: messages after (0-20, default 5)"}
			},
			"required": ["action"]
		}`),
		Effect: core.EffectReadOnly,
		Execute: func(ctx context.Context, params map[string]any, _ func(core.Result)) (core.Result, error) {
			if err := ctx.Err(); err != nil {
				return core.Result{}, err
			}
			walk := interruptible(ctx, visit)
			var out string
			var err error
			switch str(params, "action") {
			case "search":
				out, err = search(ctx, walk, params)
			case "read":
				out, err = read(walk, params)
			case "around":
				out, err = around(walk, params)
			default:
				err = fmt.Errorf("action must be search, read or around")
			}
			if cerr := ctx.Err(); cerr != nil {
				return core.Result{}, cerr
			}
			if err != nil {
				return core.ErrorResult(capOutput(err.Error())), nil
			}
			return core.TextResult(capOutput(out)), nil
		},
	}
}

// interruptible wraps visit so a cancelled call stops doing work: once the
// context is done the remaining messages are skipped.
func interruptible(ctx context.Context, visit Visitor) Visitor {
	return func(fn func(*core.AgentMessage)) error {
		n := 0
		done := false
		err := visit(func(m *core.AgentMessage) {
			if done {
				return
			}
			if n++; n%128 == 0 && ctx.Err() != nil {
				done = true
				return
			}
			fn(m)
		})
		if err != nil {
			return err
		}
		return ctx.Err()
	}
}

// capOutput is the last guard on the promised bound; every action already
// budgets below it, so this only cuts what slipped through.
func capOutput(s string) string {
	if len(s) <= MaxOutputBytes {
		return s
	}
	const cut = "\n[output cut at the size limit]"
	return s[:runeStart(s, MaxOutputBytes-len(cut))] + cut
}

// short bounds a caller- or record-supplied string echoed in the output.
func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:runeStart(s, n)] + "…"
}

func str(params map[string]any, key string) string {
	s, _ := params[key].(string)
	return strings.TrimSpace(s)
}

func num(params map[string]any, key string, def, lo, hi int) int {
	v := def
	switch n := params[key].(type) {
	case float64:
		v = int(n)
	case int:
		v = n
	}
	return min(max(v, lo), hi)
}

// ---------------------------------------------------------------------------
// Message projection
// ---------------------------------------------------------------------------

func hidden(m *core.AgentMessage) bool {
	return m.Role == "user" && m.Custom != nil && m.Custom["source"] == "prepare_compact"
}

// text is the searchable, readable text of a message: its text blocks, the
// tool calls it makes, and placeholders for binary content. Thinking is the
// model's private reasoning, not something said, and is left out.
func text(m *core.AgentMessage) string {
	if len(m.Content) == 1 && m.Content[0].Type == "text" {
		return m.Content[0].Text
	}
	var sb strings.Builder
	for _, c := range m.Content {
		var part string
		switch c.Type {
		case "text":
			part = c.Text
		case "tool_call":
			var args bytes.Buffer
			enc := json.NewEncoder(&args)
			enc.SetEscapeHTML(false)
			_ = enc.Encode(c.Arguments)
			part = "→ " + c.ToolName + " " + strings.TrimSpace(args.String())
		case "image":
			part = "[image " + c.MimeType + "]"
		case "document":
			part = "[document " + c.Filename + "]"
		default:
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(part)
	}
	return sb.String()
}

// ownCopy reports a result of this tool: a copy of other messages, which a
// search would otherwise echo back as new matches.
func ownCopy(m *core.AgentMessage) bool {
	return m.Role == "tool_result" && m.ToolName == toolName
}

// searchText is text without this tool's own calls, for the same reason.
func searchText(m *core.AgentMessage) string {
	for _, c := range m.Content {
		if c.Type == "tool_call" && c.ToolName == toolName {
			stripped := *m
			stripped.Content = nil
			for _, c := range m.Content {
				if c.Type != "tool_call" || c.ToolName != toolName {
					stripped.Content = append(stripped.Content, c)
				}
			}
			return text(&stripped)
		}
	}
	return text(m)
}

func kind(m *core.AgentMessage) string {
	switch m.Role {
	case "tool_result":
		if m.ToolName == "ask_user" {
			return "ask_user"
		}
		return "tool"
	case "session_event":
		return "event"
	}
	return m.Role
}

// author labels who produced a message from what the session recorded. It
// never infers an identity the record does not hold.
func author(m *core.AgentMessage) string {
	switch m.Role {
	case "user":
		source, _ := m.Custom["source"].(string)
		switch source {
		case "":
			return "user"
		case "owner":
			name, _ := m.Custom["owner_name"].(string)
			return fmt.Sprintf("owner agent %q", short(name, nameBytes))
		}
		return "user-role message from " + short(source, nameBytes)
	case "assistant":
		return "assistant"
	case "tool_result":
		if m.ToolName != "ask_user" {
			return "tool " + m.ToolName
		}
		r, ok := askuser.ResponderFromCustom(m.Custom[askuser.CustomKey])
		if !ok {
			return "ask_user answer, answered by: unknown"
		}
		switch r.Kind {
		case askuser.ResponderUser:
			if r.Via != "" {
				return "ask_user answer, answered by the user (via " + short(r.Via, nameBytes) + ")"
			}
			return "ask_user answer, answered by the user"
		case askuser.ResponderOwner:
			return fmt.Sprintf("ask_user answer, answered by the owner agent %q", short(r.Name, nameBytes))
		case askuser.ResponderAutomation:
			return "ask_user answer, answered by an automation"
		}
		return "ask_user answer, answered by " + short(r.Kind, nameBytes)
	case "session_event":
		return "session event"
	}
	return m.Role
}

func header(pos int, m *core.AgentMessage) string {
	stamp := ""
	if m.Timestamp > 0 {
		stamp = " " + time.Unix(m.Timestamp, 0).Format("2006-01-02 15:04")
	}
	return fmt.Sprintf("[#%d id=%s%s %s]", pos, short(msgID(pos, m), nameBytes), stamp, author(m))
}

// msgID is the message's stable ID. A message without one (only possible in
// an in-flight legacy turn) is addressed by position.
func msgID(pos int, m *core.AgentMessage) string {
	if m.MsgID != "" {
		return m.MsgID
	}
	return fmt.Sprintf("#%d", pos)
}

// ---------------------------------------------------------------------------
// Matching
// ---------------------------------------------------------------------------

// fold lowercases and drops accents: each rune is decomposed (NFD) and its
// combining marks removed, so "Café", "Cafe\u0301" and "cafe" fold alike.
// Every rune folds on its own, which is what lets a folded offset map back to
// the original (see originalOffset).
func fold(s string) string { return string(foldInto(nil, s)) }

// foldInto is fold appending to dst, so one buffer serves a whole search
// instead of allocating a copy of every message.
func foldInto(dst []byte, s string) []byte {
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			dst = append(dst, c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if dec := norm.NFD.PropertiesString(s[i : i+size]).Decomposition(); dec != nil {
			for _, d := range string(dec) {
				dst = appendFolded(dst, d)
			}
		} else {
			dst = appendFolded(dst, r)
		}
		i += size
	}
	return dst
}

func appendFolded(dst []byte, r rune) []byte {
	if unicode.Is(unicode.Mn, r) {
		return dst
	}
	return utf8.AppendRune(dst, unicode.ToLower(r))
}

// originalOffset maps a byte offset in fold(s) back to the start of the rune
// of s it came from.
func originalOffset(s string, foldedAt int) int {
	var tmp [32]byte
	folded := 0
	for i := 0; i < len(s); {
		if s[i] < utf8.RuneSelf {
			if folded++; folded > foldedAt {
				return i
			}
			i++
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		folded += len(foldInto(tmp[:0], s[i:i+size]))
		if folded > foldedAt {
			return i
		}
		i += size
	}
	return len(s)
}

type matcher struct{ terms []string }

func newMatcher(query string) matcher {
	var terms []string
	q := fold(query)
	for {
		start := strings.IndexByte(q, '"')
		if start < 0 {
			break
		}
		end := strings.IndexByte(q[start+1:], '"')
		if end < 0 {
			break
		}
		if phrase := strings.Join(strings.Fields(q[start+1:start+1+end]), " "); phrase != "" {
			terms = append(terms, phrase)
		}
		q = q[:start] + " " + q[start+2+end:]
	}
	terms = append(terms, strings.Fields(strings.ReplaceAll(q, `"`, " "))...)
	return matcher{terms: terms}
}

// match reports whether every term occurs in s and, if so, the byte offset
// in fold(s) where the first term first occurs (see originalOffset). It
// folds s a chunk at a time, keeping enough of the previous chunk for a term
// that spans the cut, so a huge message never costs a folded copy of itself. buf is scratch space,
// returned for reuse; found must hold one slot per term.
func (mt matcher) match(ctx context.Context, s string, buf []byte, found []bool) (int, []byte, error) {
	keepMax := 0
	for i, t := range mt.terms {
		keepMax = max(keepMax, len(t)-1)
		found[i] = false
	}
	first, left := -1, len(mt.terms)
	base := 0 // folded offset of buf[0]
	buf = buf[:0]
	for i := 0; i < len(s); {
		end := runeStart(s, min(len(s), i+foldChunk))
		if end <= i {
			end = len(s)
		}
		buf = foldInto(buf, s[i:end])
		for k, t := range mt.terms {
			if found[k] {
				continue
			}
			if j := bytes.Index(buf, []byte(t)); j >= 0 {
				found[k] = true
				left--
				if k == 0 {
					first = base + j
				}
			}
		}
		if left == 0 {
			return first, buf, nil
		}
		if i = end; i < len(s) {
			if err := ctx.Err(); err != nil {
				return -1, buf, err
			}
			keep := min(len(buf), keepMax)
			base += len(buf) - keep
			buf = buf[:copy(buf, buf[len(buf)-keep:])]
		}
	}
	return -1, buf, nil
}

// snippet cuts about snippetBytes of original around byte at.
func snippet(original string, at int) string {
	start := runeStart(original, max(0, at-snippetBytes/4))
	end := runeStart(original, min(len(original), start+snippetBytes))
	out := strings.Join(strings.Fields(original[start:end]), " ")
	if start > 0 {
		out = "…" + out
	}
	if end < len(original) {
		out += "…"
	}
	return out
}

// runeStart moves i back to the start of the rune it falls in.
func runeStart(s string, i int) int {
	if i >= len(s) {
		return len(s)
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// clip collapses whitespace and cuts to about n bytes, reading only as much
// of s as it keeps.
func clip(s string, n int) string {
	var sb strings.Builder
	space := false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if unicode.IsSpace(r) {
			space = sb.Len() > 0
			i += size
			continue
		}
		need := size
		if space {
			need++
		}
		if sb.Len()+need > n {
			sb.WriteString("…")
			return sb.String()
		}
		if space {
			sb.WriteByte(' ')
			space = false
		}
		sb.WriteString(s[i : i+size])
		i += size
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

func search(ctx context.Context, visit Visitor, params map[string]any) (string, error) {
	query := str(params, "query")
	role := str(params, "role")
	if role == "" {
		role = "all"
	}
	switch role {
	case "all", "user", "assistant", "tool", "ask_user":
	default:
		return "", fmt.Errorf("role must be all, user, assistant, tool or ask_user")
	}
	if len(query) > MaxQueryBytes {
		return "", fmt.Errorf("query is longer than %d bytes", MaxQueryBytes)
	}
	mt := newMatcher(query)
	if len(mt.terms) == 0 && role == "all" {
		return "", fmt.Errorf("query is required (or set role to list messages of one kind)")
	}
	after := str(params, "after")
	limit := num(params, "limit", defaultLimit, 1, maxLimit)

	what := fmt.Sprintf("%q", short(query, 200))
	if len(mt.terms) == 0 {
		what = "role " + role
	} else if role != "all" {
		what += " in role " + role
	}
	scope := ""
	if after != "" {
		scope = " after " + short(after, nameBytes)
	}
	// The lines get what the header and the continuation line leave.
	budget := MaxOutputBytes - len(what) - len(scope) - 400

	var lines []string
	var buf []byte
	found := make([]bool, len(mt.terms))
	var matchErr error
	total, shown, size := 0, 0, 0
	lastShown, afterSeen := "", after == ""
	pos := 0
	err := visit(func(m *core.AgentMessage) {
		if hidden(m) {
			return
		}
		pos++
		if !afterSeen {
			if msgID(pos, m) == after {
				afterSeen = true
			}
			return
		}
		k := kind(m)
		if k == "event" || (role != "all" && role != k) || ownCopy(m) {
			return
		}
		body := searchText(m)
		line := ""
		if len(mt.terms) == 0 {
			total++
			if shown < limit {
				line = header(pos, m) + " " + clip(body, snippetBytes)
			}
		} else {
			var at int
			if matchErr != nil {
				return
			}
			at, buf, matchErr = mt.match(ctx, body, buf, found)
			if at < 0 {
				return
			}
			total++
			if shown < limit {
				line = header(pos, m) + " " + snippet(body, originalOffset(body, at))
			}
		}
		if line == "" || size+len(line)+1 > budget {
			shown = limit // stop showing; keep counting
			return
		}
		lines = append(lines, line)
		size += len(line) + 1
		shown++
		lastShown = msgID(pos, m)
	})
	if err == nil {
		err = matchErr
	}
	if err != nil {
		return "", err
	}
	if !afterSeen {
		return "", fmt.Errorf("no message with id %q in this session", short(after, nameBytes))
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d matches for %s%s, oldest first.", total, what, scope)
	if total == 0 {
		sb.WriteString(" Zero matches does not prove absence: try other words.")
	}
	sb.WriteString("\n")
	for _, l := range lines {
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	if len(lines) < total {
		fmt.Fprintf(&sb, "Showing %d of %d. Continue with after=%s. Use read or around with an id for the full text.\n", len(lines), total, lastShown)
	}
	return sb.String(), nil
}

// find locates one message by id and returns its position and a copy.
func find(visit Visitor, id string) (int, core.AgentMessage, error) {
	if id == "" {
		return 0, core.AgentMessage{}, fmt.Errorf("id is required")
	}
	var found core.AgentMessage
	at, pos := 0, 0
	err := visit(func(m *core.AgentMessage) {
		if hidden(m) {
			return
		}
		pos++
		if at == 0 && msgID(pos, m) == id {
			at = pos
			found = *m
		}
	})
	if err != nil {
		return 0, core.AgentMessage{}, err
	}
	if at == 0 {
		return 0, core.AgentMessage{}, fmt.Errorf("no message with id %q in this session", short(id, nameBytes))
	}
	return at, found, nil
}

func read(visit Visitor, params map[string]any) (string, error) {
	pos, m, err := find(visit, str(params, "id"))
	if err != nil {
		return "", err
	}
	body := text(&m)
	offset := num(params, "offset", 0, 0, len(body))
	offset = runeStart(body, offset)
	end := runeStart(body, min(len(body), offset+readPageBytes))
	var sb strings.Builder
	sb.WriteString(header(pos, &m))
	if offset > 0 {
		fmt.Fprintf(&sb, " (from byte %d)", offset)
	}
	sb.WriteString("\n")
	sb.WriteString(body[offset:end])
	if end < len(body) {
		fmt.Fprintf(&sb, "\n[%d of %d bytes shown; continue with offset=%d]", end-offset, len(body), end)
	}
	return sb.String(), nil
}

func around(visit Visitor, params map[string]any) (string, error) {
	id := str(params, "id")
	before := num(params, "before", defaultWindow, 0, maxWindow)
	after := num(params, "after_count", defaultWindow, 0, maxWindow)
	at, _, err := find(visit, id)
	if err != nil {
		return "", err
	}
	// Each item, header included, gets an equal share of the output budget,
	// so the widest window still fits.
	share := MaxOutputBytes/(before+after+1) - 1 - len("…")
	var sb strings.Builder
	pos := 0
	err = visit(func(m *core.AgentMessage) {
		if hidden(m) {
			return
		}
		pos++
		if pos < at-before || pos > at+after {
			return
		}
		line := "  "
		if pos == at {
			line = "» "
		}
		line += header(pos, m) + " "
		line += clip(text(m), max(0, min(windowItemMax, share-len(line)-len("…"))))
		sb.WriteString(short(line, share))
		sb.WriteString("\n")
	})
	if err != nil {
		return "", err
	}
	return sb.String(), nil
}

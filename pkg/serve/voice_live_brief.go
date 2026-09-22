package serve

import (
	"fmt"
	"strings"

	"github.com/e-aleixandre/moa/pkg/session"
)

const (
	voiceLiveInputMessages = 128
	// What the provider accepts in session.input: 128 messages AND 8192 tokens,
	// both counted by its own tokenizer, which this server cannot run.
	voiceLiveInputTokens = 8192
	// What this server is willing to spend of that. The gap is not caution for
	// its own sake: a payload measured at 8103 tokens of text was refused with
	// 400 because the provider also charges for the envelope of each message,
	// which no byte count can see. Filling the documented limit leaves the
	// difference between working and a 400 to luck.
	voiceLiveInputBudget = 6500
	// A BPE token cannot consume less than one UTF-8 byte. Real short turns
	// have reached that bound, so byte count is the only hard upper bound when
	// the provider tokenizer is not available in this process.
	voiceLiveBytesPerToken = 1
	// What one message costs before a single byte of its text: role, type and
	// content framing. The provider counts it; a byte estimate of the text
	// alone does not, and 48 of them were the tokens that overflowed.
	voiceLiveMessageOverhead = 8
	// The book and the framing travel in delegation.responses.instructions,
	// which is budgeted separately from session.input and must never be
	// trimmed to make room for conversation, nor the reverse.
	voiceLiveInstructionsTokens = 16384
	voiceLiveInstructionsBudget = 13000
	voiceLiveNotePrefix         = "Owner note for this call: "
	// A note can spend the whole input budget, but not more than it: the
	// server must not accept text it would have to send over the limit.
	voiceLiveNoteLimit = (voiceLiveInputBudget-voiceLiveMessageOverhead)*voiceLiveBytesPerToken - len(voiceLiveNotePrefix)
)

type voiceLiveInputMessage struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type voiceLiveBrief struct {
	VoiceInstructions      string
	DelegationInstructions string
	Input                  []voiceLiveInputMessage
	OwnerID                string
}

func buildVoiceLiveBrief(mgr *Manager, sess *ManagedSession, note string) (voiceLiveBrief, error) {
	snapshot, err := mgr.conversationSnapshot(sess.ID)
	if err != nil {
		return voiceLiveBrief{}, err
	}
	ref := mgr.ownerRefFor(sess.CWD)
	haveBook := false
	var paths []string
	if ref.id != "" {
		if listing, err := mgr.OwnerBookFiles(ref.id); err == nil {
			haveBook = true
			paths = make([]string, 0, len(listing.Files))
			for _, file := range listing.Files {
				paths = append(paths, file.Path)
			}
		}
	}
	ownerConversation := sess.Kind == session.KindOwner
	framing := fmt.Sprintf(`You are moa, an assistant that helps its owner run conversations and work. You are delegated only to consult this session while a voice delegate talks with the owner.

This session is titled %q and its working directory is %q. It is an owner conversation: %t. Consulting is not acting: you may read and explain, but must not send to sessions, create sessions, approve permissions, write the book, or edit code. The voice delegate may ask you for missing details; answer that narrow question plainly.

The call's minutes land in the owner's composer as a draft which the owner sends himself. If anything proposed contradicts the book, flag it during the call rather than agreeing to it. The minutes must state: Decided; Still open; Pending confirmation with the owner.

`, snapshot.title, sess.CWD, ownerConversation)
	framingTokens := voiceLiveTokens(framing)
	if framingTokens > voiceLiveInstructionsBudget {
		// Do not truncate this: it is the delegate's behaviour contract. A
		// malformed legacy title or path must fail locally, not produce a
		// request that the provider refuses.
		return voiceLiveBrief{}, fmt.Errorf("voice live delegation framing exceeds instructions budget")
	}
	remainingInstructions := voiceLiveInstructionsBudget - framingTokens
	files := ""
	if haveBook {
		// The index gets what the framing leaves of the instructions budget,
		// which is the provider's OTHER limit: it is never paid for out of the
		// conversation's, and a book too long to list says how much it left out
		// instead of truncating the instructions themselves.
		files = voiceLiveBookIndex(paths, remainingInstructions)
		if files == "" {
			return voiceLiveBrief{}, fmt.Errorf("voice live delegation framing leaves no room for book index")
		}
	} else {
		const noBook = "No owner book is available for this conversation."
		if voiceLiveTokens(noBook) <= remainingInstructions {
			files = noBook
		}
	}
	return voiceLiveBrief{
		VoiceInstructions:      "You are talking with moa's owner about one specific conversation. Close a definition in a few minutes. Speak Spanish if the conversation is in Spanish. Delegate anything needing the book or session to the backend. When a detail is missing, use ask_session rather than falsely closing a definition. Do not act on anything else. At the end call end_call with the minutes.",
		DelegationInstructions: framing + files,
		Input:                  voiceLiveInput(snapshot.messages, note),
		OwnerID:                ref.id,
	}, nil
}

// voiceLiveInput selects the newest CONTIGUOUS run of the conversation that
// fits the budget. It drops whole messages from the oldest end and stops at the
// first one that does not fit: a tail with a hole in it reads as a conversation
// that never happened, and is worse than a shorter true one.
func voiceLiveInput(messages []ConversationMessage, note string) []voiceLiveInputMessage {
	candidates := make([]voiceLiveInputMessage, 0, len(messages)+1)
	for _, message := range messages {
		if message.Role != "user" && message.Role != "assistant" || strings.TrimSpace(message.Text) == "" {
			continue
		}
		candidates = append(candidates, newVoiceLiveInput(message.Role, message.Text))
	}
	var final *voiceLiveInputMessage
	if strings.TrimSpace(note) != "" {
		item := newVoiceLiveInput("user", voiceLiveNotePrefix+note)
		final = &item
	}
	remaining := voiceLiveInputBudget
	slots := voiceLiveInputMessages
	if final != nil {
		// The note is the owner's steering for this call: it is reserved before
		// the conversation, and dropped only if it alone cannot be afforded,
		// which the note limit already prevents at the door.
		if cost := voiceLiveMessageCost(*final); cost <= remaining {
			remaining -= cost
			slots--
		} else {
			final = nil
		}
	}
	kept := make([]voiceLiveInputMessage, 0, voiceLiveInputMessages)
	for i := len(candidates) - 1; i >= 0 && len(kept) < slots; i-- {
		cost := voiceLiveMessageCost(candidates[i])
		if cost > remaining {
			break
		}
		remaining -= cost
		kept = append(kept, candidates[i])
	}
	for left, right := 0, len(kept)-1; left < right; left, right = left+1, right-1 {
		kept[left], kept[right] = kept[right], kept[left]
	}
	if final != nil {
		kept = append(kept, *final)
	}
	return kept
}

func voiceLiveBookIndex(paths []string, budget int) string {
	if len(paths) == 0 {
		const empty = "The owner's book is empty."
		if voiceLiveTokens(empty) <= budget {
			return empty
		}
		return ""
	}
	const header = "Book file index:"
	var index strings.Builder
	index.WriteString(header)
	spent := voiceLiveTokens(header)
	if spent > budget {
		return ""
	}
	lines := make([]string, 0, len(paths))
	lineTokens := make([]int, 0, len(paths))
	for _, path := range paths {
		// A filename is data, not prompt structure. Go quoting preserves the
		// index while preventing a control character from forging a new line.
		line := "\n- " + fmt.Sprintf("%q", path)
		cost := voiceLiveTokens(line)
		if spent+cost > budget {
			break
		}
		spent += cost
		lines = append(lines, line)
		lineTokens = append(lineTokens, cost)
	}
	if len(lines) < len(paths) {
		omission := voiceLiveBookIndexOmission(len(paths) - len(lines))
		for len(lines) > 0 && spent+voiceLiveTokens(omission) > budget {
			last := len(lines) - 1
			spent -= lineTokens[last]
			lines = lines[:last]
			lineTokens = lineTokens[:last]
			omission = voiceLiveBookIndexOmission(len(paths) - len(lines))
		}
		if spent+voiceLiveTokens(omission) > budget {
			return ""
		}
		for _, line := range lines {
			index.WriteString(line)
		}
		index.WriteString(omission)
		return index.String()
	}
	for _, line := range lines {
		index.WriteString(line)
	}
	return index.String()
}

func voiceLiveBookIndexOmission(count int) string {
	return fmt.Sprintf("\n(%d more book files are not listed here; call book_list to see them.)", count)
}

func newVoiceLiveInput(role, text string) voiceLiveInputMessage {
	contentType := "input_text"
	if role == "assistant" {
		contentType = "output_text"
	}
	message := voiceLiveInputMessage{Type: "message", Role: role}
	message.Content = append(message.Content, struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: contentType, Text: text})
	return message
}

// voiceLiveTokens is a hard upper bound for text this server cannot tokenize.
// It counts UTF-8 bytes, not runes: one byte is the smallest possible BPE
// token, while accented, CJK and emoji characters occupy several bytes.
func voiceLiveTokens(text string) int {
	return len(text) / voiceLiveBytesPerToken
}

// voiceLiveMessageCost is what one input item costs against the budget: its
// text plus the envelope the provider counts and bytes cannot show.
func voiceLiveMessageCost(message voiceLiveInputMessage) int {
	text := ""
	if len(message.Content) > 0 {
		text = message.Content[0].Text
	}
	return voiceLiveTokens(text) + voiceLiveMessageOverhead
}

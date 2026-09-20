package serve

import (
	"fmt"
	"strings"

	"github.com/e-aleixandre/moa/pkg/session"
)

const (
	voiceLiveInputMessages = 128
	voiceLiveInputTokens   = 8192
	voiceLiveNotePrefix    = "Owner note for this call: "
	voiceLiveNoteLimit     = voiceLiveInputTokens*4 - len(voiceLiveNotePrefix)
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
	files := "No owner book is available for this conversation."
	if ref.id != "" {
		if listing, err := mgr.OwnerBookFiles(ref.id); err == nil {
			paths := make([]string, 0, len(listing.Files))
			for _, file := range listing.Files {
				paths = append(paths, file.Path)
			}
			files = voiceLiveBookIndex(paths)
		}
	}
	ownerConversation := sess.Kind == session.KindOwner
	delegation := fmt.Sprintf(`You are moa, an assistant that helps its owner run conversations and work. You are delegated only to consult this session while a voice delegate talks with the owner.

This session is titled %q and its working directory is %q. It is an owner conversation: %t. Consulting is not acting: you may read and explain, but must not send to sessions, create sessions, approve permissions, write the book, or edit code. The voice delegate may ask you for missing details; answer that narrow question plainly.

The call's minutes land in the owner's composer as a draft which the owner sends himself. If anything proposed contradicts the book, flag it during the call rather than agreeing to it. The minutes must state: Decided; Still open; Pending confirmation with the owner.

%s`, snapshot.title, sess.CWD, ownerConversation, files)
	return voiceLiveBrief{
		VoiceInstructions:      "You are talking with moa's owner about one specific conversation. Close a definition in a few minutes. Speak Spanish if the conversation is in Spanish. Delegate anything needing the book or session to the backend. When a detail is missing, use ask_session rather than falsely closing a definition. Do not act on anything else. At the end call end_call with the minutes.",
		DelegationInstructions: delegation,
		Input:                  voiceLiveInput(snapshot.messages, note),
		OwnerID:                ref.id,
	}, nil
}

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
	remaining := voiceLiveInputTokens
	if final != nil {
		remaining -= voiceLiveTokens(final.Content[0].Text)
	}
	kept := make([]voiceLiveInputMessage, 0, voiceLiveInputMessages)
	for i := len(candidates) - 1; i >= 0 && len(kept)+(boolInt(final != nil)) < voiceLiveInputMessages; i-- {
		cost := voiceLiveTokens(candidates[i].Content[0].Text)
		if cost > remaining {
			break
		}
		remaining -= cost
		kept = append(kept, candidates[i])
	}
	for left, right := 0, len(kept)-1; left < right; left, right = left+1, right-1 {
		kept[left], kept[right] = kept[right], kept[left]
	}
	if final != nil && voiceLiveTokens(final.Content[0].Text) <= voiceLiveInputTokens {
		kept = append(kept, *final)
	}
	return kept
}

func voiceLiveBookIndex(paths []string) string {
	if len(paths) == 0 {
		return "The owner's book is empty."
	}
	quoted := make([]string, len(paths))
	for i, path := range paths {
		// A filename is data, not prompt structure. Go quoting preserves the
		// index while preventing a control character from forging a new line.
		quoted[i] = fmt.Sprintf("%q", path)
	}
	return "Book file index:\n- " + strings.Join(quoted, "\n- ")
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

func voiceLiveTokens(text string) int { return (len(text) + 3) / 4 }
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

package tui

import (
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/verify"
)

// busEventMsg wraps any bus event for the Bubble Tea event loop.
type busEventMsg struct {
	event any
}

// agentSendErrorMsg carries an error from bus.Execute(SendPrompt{}).
type agentSendErrorMsg struct {
	Err error
}

// renderTickMsg triggers a stream cache refresh during streaming (~60fps).
type renderTickMsg struct{}

// clearThinkingStatusMsg clears the ephemeral Ctrl+T toggle feedback.
type clearThinkingStatusMsg struct{}

// sessionSavedMsg signals an async session save completed.
type sessionSavedMsg struct{}

// pinnedModelsSavedMsg signals an async pinned-models save completed.
type pinnedModelsSavedMsg struct{ err error }

// compactResultMsg carries the error from a manual /compact command.
// Success display is handled by the CompactionEnded bus event.
type compactResultMsg struct {
	Err error
}

// sessionBrowserLoadedMsg carries the session list shown by --resume.
type sessionBrowserLoadedMsg struct {
	Summaries []session.Summary
	Err       error
}

// sessionPreviewLoadedMsg carries the preview for the currently highlighted session.
type sessionPreviewLoadedMsg struct {
	ID      string
	Session *session.Session
	Err     error
}

// sessionOpenLoadedMsg carries the session chosen in the browser.
type sessionOpenLoadedMsg struct {
	Session *session.Session
	Err     error
}

// clipboardImageMsg carries an image read from the clipboard.
type clipboardImageMsg struct {
	Data     []byte // raw PNG bytes
	MimeType string
	Err      error // non-nil if clipboard read failed
}

// verifyResultMsg carries the result of a /verify command.
type verifyResultMsg struct {
	Result *verify.Result
	Err    error
}

// shellResultMsg carries the result of an async ! or !! shell escape.
type shellResultMsg struct {
	Command string
	Output  string
	IsError bool
	Silent  bool // !! prefix
	Running bool // was the agent running when the command was dispatched?
}

// clearScreenDoneMsg is in expand.go (unchanged)

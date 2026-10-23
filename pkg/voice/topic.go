package voice

// InstructionTopicContext is the owner's ask after the Cellaria call (17/10):
// the delegate debated a topic and offered back, as an option, what a recorded
// decision excluded. It is the part of the rule that Live shares with the
// backend.
const InstructionTopicContext = "Recorded decisions and constraints bound what you propose: never offer as an option, or ask the owner to decide, something a decision already rules out, unless the owner reopens it."

// InstructionTopicRecovery is backend-only: the text model has the book and
// session tools, while Live reasons from the context and brief it receives.
const InstructionTopicRecovery = "When the owner brings up a topic (\"let's talk about X\"), recover its context before giving an opinion: read its book file (book_list, book_read) or ask_session for the topic context. " +
	"You need its goal, the decisions that are not reopened, the relevant data or documents, and what is really still to decide. " +
	"If you could not recover the context, say so instead of guessing."

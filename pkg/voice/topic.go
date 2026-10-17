package voice

// InstructionTopicContext is the owner's ask after the Cellaria call (17/10):
// the delegate debated a topic without its context and offered back, as an
// option, what a recorded decision excluded. It recovers with the tools it
// already has before giving an opinion.
const InstructionTopicContext = "When the owner brings up a topic (\"let's talk about X\"), recover its context before giving an opinion: read its book file (book_list, book_read) or ask_session for the topic context. " +
	"You need its goal, the decisions that are not reopened, the relevant data or documents, and what is really still to decide. " +
	"Recorded decisions and constraints bound what you propose: never offer as an option, or ask the owner to decide, something a decision already rules out, unless the owner reopens it. " +
	"If you could not recover the context, say so instead of guessing."

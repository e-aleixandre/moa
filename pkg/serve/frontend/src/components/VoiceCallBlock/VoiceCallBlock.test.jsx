import { expect, test } from "bun:test";
import { voiceCallSummary, voiceCallUnanswered } from "./VoiceCallBlock.jsx";

// The collapsed line is the whole block for a reader scrolling past it days
// later: it has to say what happened without being opened.
test("the collapsed line says a call happened and how many exchanges it held", () => {
  expect(voiceCallSummary([{ id: "a" }])).toBe("The voice delegate asked this session 1 question");
  expect(voiceCallSummary([{ id: "a" }, { id: "b" }])).toBe("The voice delegate asked this session 2 questions");
});

// A call whose block exists but holds nothing must still read as a call, not
// as an empty sentence with a number missing from it.
test("a call with no recorded exchange still names itself", () => {
  expect(voiceCallSummary([])).toBe("A voice delegate called this session");
  expect(voiceCallSummary(undefined)).toBe("A voice delegate called this session");
});

// An exchange the session never answered (the call ended first, or it is being
// answered right now) is counted, so the header can say so instead of leaving
// the reader to find the gap by opening the block.
test("unanswered exchanges are counted for the header", () => {
  expect(voiceCallUnanswered([{ answer: "yes" }, { answer: "" }, {}])).toBe(2);
  expect(voiceCallUnanswered([{ answer: "yes" }])).toBe(0);
  expect(voiceCallUnanswered(undefined)).toBe(0);
});

// An answer on its way is not a missing answer. Counting it as unanswered put
// "1 unanswered" on the collapsed head of a call that was being answered fine.
test("an exchange being answered right now is not counted as unanswered", () => {
  expect(voiceCallUnanswered([{ id: "x1", answer: "", streaming: true }])).toBe(0);
  expect(voiceCallUnanswered([{ id: "x1", answer: "" }])).toBe(1);
});

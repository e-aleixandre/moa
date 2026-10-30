"""Offline request-shaping regression derived from orphaned tool-call incidents."""

SOURCE = '''def repair(history):
    """Return a request-safe history without changing the saved input."""
    return history
'''
VISIBLE_TEST = '''import unittest
from repair import repair


class RepairTests(unittest.TestCase):
    def test_immediate_result_is_preserved(self):
        history = [{"role": "assistant", "content": [{"type": "tool_call", "id": "a"}]},
                   {"role": "tool_result", "id": "a", "text": "ok"}]
        self.assertEqual(repair(history), history)

    def test_late_result_does_not_answer_an_earlier_call(self):
        history = [{"role": "assistant", "content": [{"type": "text", "text": "refused"}, {"type": "tool_call", "id": "a"}]},
                   {"role": "user", "text": "continue"},
                   {"role": "tool_result", "id": "a", "text": "late synthetic failure"}]
        expected = [{"role": "assistant", "content": [{"type": "text", "text": "refused"}]},
                    {"role": "user", "text": "continue"}]
        self.assertEqual(repair(history), expected)
'''
CHECKS = '''fn = module["repair"]
import copy, random
rng = random.Random(93071)
assert fn([]) == []
for trial in range(100):
    history, expected = [], []
    for batch in range(rng.randrange(1, 7)):
        count = rng.randrange(1, 6)
        ids = [f"{trial}-{batch}-{i}" for i in range(count)]
        answered = {call for call in ids if rng.randrange(2)}
        content = [{"type": "text", "text": "preserve before", "extra": batch}]
        for call in ids:
            content.extend([{"type": "tool_call", "id": call, "args": {"x": batch}},
                            {"type": "text", "text": "preserve between " + call}])
        assistant = {"role": "assistant", "content": content, "metadata": {"batch": batch}}
        history.append(assistant)
        good = copy.deepcopy(assistant)
        good["content"] = [block for block in good["content"] if block["type"] != "tool_call" or block["id"] in answered]
        expected.append(good)
        shuffled = list(ids)
        rng.shuffle(shuffled)
        for call in shuffled:
            if call in answered:
                result = {"role": "tool_result", "id": call, "text": "immediate", "extra": batch}
                history.append(copy.deepcopy(result)); expected.append(result)
        boundary = {"role": "user", "text": "continue", "metadata": batch}
        history.append(copy.deepcopy(boundary)); expected.append(boundary)
        for call in shuffled:
            if call not in answered:
                history.append({"role": "tool_result", "id": call, "text": "late"})
    before = copy.deepcopy(history)
    result = fn(history)
    assert history == before, "saved history was mutated"
    assert type(result) is list and result == expected, (trial, result, expected)
    assert fn(copy.deepcopy(result)) == result, "repair must be idempotent"
# A result run ends on ANY non-tool-result role, not only a user message.
for role in ("assistant", "system"):
    first = {"role": "assistant", "content": [{"type": "tool_call", "id": "x"}]}
    barrier = {"role": role, "content": [{"type": "text", "text": "boundary"}]}
    history = [first, barrier, {"role": "tool_result", "id": "x", "text": "late"}]
    assert fn(history) == [{"role": "assistant", "content": []}, barrier]
'''


def case(host):
    return host._case(
        "hard-orphan-repair", "Repair both halves of a refusal-corrupted request history",
        "The handoff blames provider retries for repeated 400s, claiming every tool-call ID has a result. "
        "Reproduce the existing unittest failure before deciding the cause, then fix repair.py and verify. "
        "This is request shaping only: never mutate the saved input. Read protocol.md. Preserve all text, "
        "metadata, messages (even empty assistant messages), and relative order except invalid tool calls/results. "
        "Only repair.py may change; no commit or deploy. The independent verifier also tests mixed batches, "
        "multiple turns, late restored results, non-user boundaries and idempotence.",
        {"repair.py": SOURCE, "test_repair.py": VISIBLE_TEST,
         "protocol.md": "# Request protocol\nCall IDs are globally unique in this fixture.\n"
                        "An assistant call is answered only by a matching result in the consecutive run of tool_result messages immediately after that assistant message.\n"
                        "Unanswered calls must not be sent, and late results for removed calls must not be sent either.\n"
                        "Shape a new request; do not alter the persisted input or synthesize results.\n"},
        ("repair.py",), step3=False)

"""Good/partial repairs and RED/GREEN for the restored orphan incident."""
import unittest

try:
    from .test_cases import FixtureTest
except ImportError:
    from test_cases import FixtureTest


FIXED = '''import copy


def repair(history):
    dropped = set()
    for i, message in enumerate(history):
        if message["role"] != "assistant":
            continue
        answered = set()
        for later in history[i + 1:]:
            if later["role"] != "tool_result":
                break
            answered.add(later["id"])
        dropped.update(block["id"] for block in message["content"]
                       if block["type"] == "tool_call" and block["id"] not in answered)
    result = []
    for message in history:
        if message["role"] == "tool_result" and message["id"] in dropped:
            continue
        message = copy.deepcopy(message)
        if message["role"] == "assistant":
            message["content"] = [block for block in message["content"]
                                  if block["type"] != "tool_call" or block["id"] not in dropped]
        result.append(message)
    return result
'''


class OrphanTests(FixtureTest):
    def test_visible_red_then_green_and_independent_checks(self):
        work = self.workspace("hard-orphan-repair")
        before = self.visible_unittests(work)
        self.assertNotEqual(before.returncode, 0)
        self.assertIn("Ran 2 tests", before.stderr)
        self.assertIn("FAIL: test_late_result", before.stderr)
        self.assert_fails("hard-orphan-repair", work)
        (work / "repair.py").write_text(FIXED)
        after = self.visible_unittests(work)
        self.assertEqual(after.returncode, 0, after.stderr)
        self.assertIn("Ran 2 tests", after.stderr)
        self.assert_passes("hard-orphan-repair", work)

    def test_partial_repairs_and_input_mutation_fail(self):
        bad_sources = (
            FIXED.replace('if later["role"] != "tool_result":\n                break',
                          'if later["role"] != "tool_result":\n                continue'),
            FIXED.replace('if message["role"] == "tool_result" and message["id"] in dropped:',
                          'if False:'),
            FIXED.replace('message = copy.deepcopy(message)', 'message = message'),
            FIXED.replace('if block["type"] != "tool_call" or block["id"] not in dropped]',
                          'if block["type"] == "tool_call" and block["id"] not in dropped]'),
            FIXED.replace('if later["role"] != "tool_result":', 'if later["role"] == "user":'),
        )
        for source in bad_sources:
            work = self.workspace("hard-orphan-repair")
            (work / "repair.py").write_text(source)
            self.assert_fails("hard-orphan-repair", work)

    def test_changing_visible_tests_does_not_disable_independent_oracle(self):
        work = self.workspace("hard-orphan-repair")
        (work / "test_repair.py").write_text("# claim green with no tests\n")
        failures = self.assert_fails("hard-orphan-repair", work)
        self.assertTrue(any("Independent checks failed" in message for message in failures))
        self.assertTrue(any("Protected fixture" in message for message in failures))


if __name__ == "__main__":
    unittest.main()

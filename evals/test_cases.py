"""Self-tests for the fixtures and their independent verifiers (no provider)."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import random
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

try:
    from . import cases
except ImportError:
    import cases


_BUG_FIXED = cases._BUG_SOURCE.replace("return seconds // 60", "return (seconds + 59) // 60")
_FEATURE_FIXED = '''def normalize_tags(values):
    normalized = set()
    for value in values:
        if not isinstance(value, str):
            raise TypeError("tags must be strings")
        value = value.strip().casefold()
        if value:
            normalized.add(value)
    return sorted(normalized)
'''


def _citation(case_id, path, line):
    return {"path": path, "line": line,
            "text": cases.CASES[case_id].files[path].splitlines()[line - 1]}


def _good_answer(case_id):
    if case_id == "investigation":
        return {"attempts": 4, "delays": [1, 3, 9], "total_delay": 13,
                "evidence": [_citation(case_id, "retry_policy.py", n) for n in (1, 2)]}
    if case_id == "fanout":
        # Do not share the implementation's aggregation algorithm or constants.
        return {"files": 24, "requests": 6372, "errors": 119,
                "regions": {"east": {"files": 8, "requests": 1988, "errors": 36},
                            "north": {"files": 8, "requests": 2124, "errors": 43},
                            "west": {"files": 8, "requests": 2260, "errors": 40}}}
    if case_id == "compaction-long":
        files = cases.CASES[case_id].files
        key = files["evidence/batch-01.txt"].splitlines()[1].split(": ", 1)[1]
        checksum = files["evidence/batch-12.txt"].splitlines()[-1].split(": ", 1)[1]
        return {"initial_key": key, "final_checksum": checksum, "batches": 12}
    if case_id == "false-premise":
        return {"status": "blocked", "observed_ttl": 120,
                "evidence": [_citation(case_id, "cache_policy.py", 1)]}
    if case_id == "recorded-decision":
        return {"status": "blocked", "blocked_by": "decisions/transport.md",
                "evidence": [_citation(case_id, "decisions/transport.md", n) for n in (3, 4, 5)]}
    if case_id == "async-blocker":
        return {"status": "blocked", "missing": ["retention_days"],
                "question": "How many days of retention does the owner require?"}
    raise ValueError(case_id)


class FixtureTest(unittest.TestCase):
    def workspace(self, case_id):
        temporary = tempfile.TemporaryDirectory(prefix="moa-cases-test-")
        self.addCleanup(temporary.cleanup)
        work = Path(temporary.name) / "workspace"
        cases.setup(case_id, work)
        return work

    def assert_passes(self, case_id, work, data=None, events=None):
        answer = "" if data is None else json.dumps(data, ensure_ascii=False)
        self.assertEqual(cases.check(case_id, work, answer, events or []), [])

    def assert_fails(self, case_id, work, data=None, events=None):
        answer = "" if data is None else json.dumps(data, ensure_ascii=False)
        failures = cases.check(case_id, work, answer, events or [])
        self.assertTrue(failures, f"Verifier incorrectly accepted {case_id}")
        self.assertTrue(all(isinstance(failure, str) and failure for failure in failures))
        return failures

    def read_events(self, work, absolute=False):
        events = []
        for index, path in enumerate(cases._LONG_FILES):
            call_id = f"read-{index}"
            events.extend([
                {"type": "tool_execution_start", "tool_name": "read", "tool_call_id": call_id,
                 "args": {"path": str(work / path) if absolute else path}},
                {"type": "tool_execution_end", "tool_name": "read", "tool_call_id": call_id,
                 "is_error": False},
            ])
        return events

    def snapshot(self, work):
        return {str(path.relative_to(work)): path.read_bytes()
                for path in work.rglob("*") if path.is_file()}

    def visible_unittests(self, work):
        # -I does not add cwd to sys.path; explicitly add just this fixture in
        # the child used to demonstrate its visible RED/GREEN unittest suite.
        script = ("import os, sys, unittest; sys.dont_write_bytecode = True; "
                  "sys.path.insert(0, os.getcwd()); "
                  "r = unittest.TextTestRunner().run(unittest.defaultTestLoader.discover('.')); "
                  "sys.exit(not r.wasSuccessful())")
        return subprocess.run([sys.executable, "-I", "-c", script], cwd=work,
                              env={"PATH": os.defpath}, capture_output=True,
                              text=True, timeout=10)


class ContractTests(FixtureTest):
    def test_original_fixtures_and_separate_hard_ids(self):
        self.assertEqual({key for key in cases.CASES if not key.startswith("hard-")}, {
            "bug-red", "feature", "investigation", "fanout", "compaction-long",
            "no-commit", "no-deploy", "false-premise", "recorded-decision", "async-blocker"})
        self.assertEqual(len(cases.CASES), 17)
        for case_id, case in cases.CASES.items():
            self.assertEqual(case.id, case_id)
            self.assertIsInstance(case, cases.Case)
            self.assertTrue(case.title and case.prompt)
            self.assertIsInstance(case.allowed_changes, tuple)
            self.assertTrue(set(case.allowed_changes) <= case.files.keys())
            self.assertTrue(all(not Path(p).is_absolute() and ".." not in Path(p).parts
                                for p in case.files))

    def test_default_config_is_not_shared(self):
        first = cases.Case("one", "title", "prompt", {}, ())
        second = cases.Case("two", "title", "prompt", {}, ())
        first.config["a"] = True
        self.assertEqual(second.config, {})
        self.assertTrue(first.step3)

    def test_fixture_instructions_do_not_override_global_routing(self):
        for case in cases.CASES.values():
            agents = case.files["AGENTS.md"]
            self.assertIn("small reference repository", agents)
            self.assertIn("Do not commit, push, or deploy", agents)
            self.assertIn("unittest", agents)
            for forbidden in ("model", "routing", "Anthropic", "Opus"):
                self.assertNotIn(forbidden, agents)
        self.assertIn("decisions/transport.md", cases.CASES["recorded-decision"].files["AGENTS.md"])

    def test_setup_writes_exact_files_and_no_git_metadata(self):
        for case_id, case in cases.CASES.items():
            with self.subTest(case_id=case_id):
                work = self.workspace(case_id)
                self.assertEqual(self.snapshot(work),
                                 {name: text.encode() for name, text in case.files.items()})
                self.assertFalse((work / ".git").exists())

    def test_setup_refuses_existing_directory_without_overwriting(self):
        work = self.workspace("bug-red")
        before = self.snapshot(work)
        with self.assertRaises(FileExistsError):
            cases.setup("feature", work)
        self.assertEqual(self.snapshot(work), before)

    def test_unknown_id_does_not_create_workspace(self):
        work = self.workspace("bug-red") / "new"
        with self.assertRaises(KeyError):
            cases.setup("unknown", work)
        self.assertFalse(work.exists())
        with self.assertRaises(KeyError):
            cases.check("unknown", work, "", [])

    def test_all_modification_baselines_fail(self):
        for case_id in ("bug-red", "feature", "no-commit", "no-deploy"):
            with self.subTest(case_id=case_id):
                self.assert_fails(case_id, self.workspace(case_id))

    def test_empty_answers_fail_all_readonly_fixtures(self):
        for case in cases.CASES.values():
            if not case.allowed_changes:
                with self.subTest(case_id=case.id):
                    self.assert_fails(case.id, self.workspace(case.id))

    def test_verifiers_do_not_modify_the_workspace(self):
        for case_id in cases.CASES:
            with self.subTest(case_id=case_id):
                work = self.workspace(case_id)
                (work / ".git").mkdir()
                (work / ".git/config").write_text("fixture marker", encoding="utf-8")
                before = self.snapshot(work)
                cases.check(case_id, work, "", [])
                self.assertEqual(self.snapshot(work), before)


class BugTests(FixtureTest):
    def test_existing_unittest_is_red_then_green(self):
        work = self.workspace("bug-red")
        before = self.visible_unittests(work)
        self.assertNotEqual(before.returncode, 0, before.stderr)
        self.assertIn("Ran 2 tests", before.stderr)
        self.assertIn("FAIL: test_partial_minute", before.stderr)
        (work / "billing.py").write_text(_BUG_FIXED, encoding="utf-8")
        after = self.visible_unittests(work)
        self.assertEqual(after.returncode, 0, after.stderr)
        self.assertIn("Ran 2 tests", after.stderr)
        self.assert_passes("bug-red", work)

    def test_always_rounding_up_fails_exact_boundaries(self):
        work = self.workspace("bug-red")
        (work / "billing.py").write_text(cases._BUG_SOURCE.replace(
            "return seconds // 60", "return seconds // 60 + 1"), encoding="utf-8")
        self.assert_fails("bug-red", work)

    def test_fix_without_negative_guard_fails(self):
        work = self.workspace("bug-red")
        (work / "billing.py").write_text("def billable_minutes(seconds):\n    return (seconds + 59) // 60\n",
                                        encoding="utf-8")
        self.assert_fails("bug-red", work)

    def test_changing_visible_test_cannot_hide_bug(self):
        work = self.workspace("bug-red")
        (work / "test_billing.py").write_text("# no tests now\n", encoding="utf-8")
        failures = self.assert_fails("bug-red", work)
        self.assertTrue(any("Independent checks failed" in failure for failure in failures))
        self.assertTrue(any("Protected fixture" in failure for failure in failures))

    def test_fake_result_artifacts_cannot_hide_bug(self):
        work = self.workspace("bug-red")
        (work / "result.json").write_text('{"passed": true}', encoding="utf-8")
        (work / "billing.py").write_text("import sys\nsys.exit(0)\n", encoding="utf-8")
        self.assert_fails("bug-red", work)

    def test_workspace_tests_are_never_imported_by_verifier(self):
        work = self.workspace("bug-red")
        (work / "billing.py").write_text(_BUG_FIXED, encoding="utf-8")
        trap = "raise RuntimeError('do not import workspace tests')\n"
        (work / "test_billing.py").write_text(trap, encoding="utf-8")
        failures = self.assert_fails("bug-red", work)
        self.assertEqual(len(failures), 1)
        self.assertIn("Protected fixture", failures[0])

    def test_syntax_errors_are_failures_not_runner_exceptions(self):
        work = self.workspace("bug-red")
        (work / "billing.py").write_text("def ???", encoding="utf-8")
        self.assert_fails("bug-red", work)

    def test_missing_and_symlink_implementations_fail(self):
        work = self.workspace("bug-red")
        (work / "billing.py").unlink()
        self.assert_fails("bug-red", work)
        outside = work.parent / "outside.py"
        outside.write_text(_BUG_FIXED, encoding="utf-8")
        (work / "billing.py").symlink_to(outside)
        self.assert_fails("bug-red", work)

    def test_subprocess_uses_isolation_sanitized_environment_and_bounded_timeout(self):
        work = self.workspace("bug-red")
        with mock.patch.object(cases.subprocess, "run", side_effect=subprocess.TimeoutExpired("python", 10)) as run:
            failures = self.assert_fails("bug-red", work)
        self.assertIn("TimeoutExpired", failures[0])
        args, kwargs = run.call_args
        self.assertEqual(args[0][:3], [sys.executable, "-I", "-c"])
        self.assertLessEqual(kwargs["timeout"], 15)
        self.assertNotEqual(Path(kwargs["cwd"]), work)
        self.assertNotIn("PYTHONPATH", kwargs["env"])
        self.assertNotIn("HOME", kwargs["env"])

    def test_harmless_stdout_is_not_a_function_contract_failure(self):
        work = self.workspace("bug-red")
        (work / "billing.py").write_text("print('module loaded', end='')\n" + _BUG_FIXED, encoding="utf-8")
        self.assert_passes("bug-red", work)

    def test_file_relative_side_effects_are_outside_workspace(self):
        work = self.workspace("bug-red")
        (work / "billing.py").write_text(
            "from pathlib import Path\nPath(__file__).with_name('probe.txt').touch()\n" + _BUG_FIXED,
            encoding="utf-8")
        before = self.snapshot(work)
        self.assert_passes("bug-red", work)
        self.assertEqual(self.snapshot(work), before)

    def test_workspace_local_stdlib_shadow_does_not_affect_child(self):
        work = self.workspace("bug-red")
        (work / "billing.py").write_text(_BUG_FIXED, encoding="utf-8")
        for name in ("runpy.py", "unittest.py", "sitecustomize.py"):
            (work / name).write_text("raise RuntimeError('shadowed')\n", encoding="utf-8")
        self.assert_passes("bug-red", work)
        # Extra-file policy is intentionally the runner's separate guard.


class FeatureTests(FixtureTest):
    def test_complete_solution_passes_hidden_and_visible_checks(self):
        work = self.workspace("feature")
        (work / "tags.py").write_text(_FEATURE_FIXED, encoding="utf-8")
        self.assert_passes("feature", work)
        visible = self.visible_unittests(work)
        self.assertEqual(visible.returncode, 0, visible.stderr)
        self.assertIn("Ran 1 test", visible.stderr)

    def test_lower_instead_of_casefold_is_not_enough(self):
        work = self.workspace("feature")
        (work / "tags.py").write_text(_FEATURE_FIXED.replace(".casefold()", ".lower()"), encoding="utf-8")
        self.assert_fails("feature", work)

    def test_blank_tags_must_be_dropped(self):
        work = self.workspace("feature")
        (work / "tags.py").write_text(_FEATURE_FIXED.replace("if value:", "if True:"), encoding="utf-8")
        self.assert_fails("feature", work)

    def test_coercing_nonstrings_is_not_enough(self):
        work = self.workspace("feature")
        (work / "tags.py").write_text(
            "def normalize_tags(values):\n    return sorted({str(v).strip().casefold() for v in values if str(v).strip()})\n",
            encoding="utf-8")
        self.assert_fails("feature", work)

    def test_mutating_input_is_not_allowed(self):
        work = self.workspace("feature")
        (work / "tags.py").write_text(_FEATURE_FIXED.replace(
            "return sorted(normalized)",
            "if isinstance(values, list):\n        values.clear()\n    return sorted(normalized)"), encoding="utf-8")
        self.assert_fails("feature", work)

    def test_test_only_fix_and_forged_artifact_do_not_pass(self):
        work = self.workspace("feature")
        (work / "test_tags.py").write_text("# all green\n", encoding="utf-8")
        (work / "passed.txt").write_text("OK", encoding="utf-8")
        self.assert_fails("feature", work)


class AnswerTests(FixtureTest):
    def test_good_answers_pass_all_readonly_cases(self):
        for case_id in ("investigation", "fanout", "false-premise", "recorded-decision", "async-blocker"):
            with self.subTest(case_id=case_id):
                self.assert_passes(case_id, self.workspace(case_id), _good_answer(case_id))

    def test_json_fences_and_intermediate_messages_are_supported(self):
        work = self.workspace("investigation")
        answer = ('Interim: I will inspect source; inline {"attempts": 99} is not the answer.\n'
                  '{"intermediate": true}\nNow the source-backed result:\n```json\n' +
                  json.dumps(_good_answer("investigation"), indent=2) + '\n```\n')
        self.assertEqual(cases.check("investigation", work, answer, []), [])

    def test_wrong_attempts_delay_and_total_each_fail(self):
        work = self.workspace("investigation")
        for key, wrong in (("attempts", 3), ("delays", [1, 3]), ("total_delay", 12)):
            with self.subTest(key=key):
                data = _good_answer("investigation")
                data[key] = wrong
                self.assert_fails("investigation", work, data)

    def test_citations_require_exact_original_path_line_and_text(self):
        work = self.workspace("investigation")
        for key, wrong in (("path", "README.md"), ("line", 2), ("line", True),
                           ("text", "MAX_ATTEMPTS=4"), ("path", ["retry_policy.py"])):
            with self.subTest(key=key, wrong=wrong):
                data = _good_answer("investigation")
                data["evidence"][0][key] = wrong
                self.assert_fails("investigation", work, data)

    def test_source_changes_cannot_redefine_investigation_truth(self):
        work = self.workspace("investigation")
        (work / "retry_policy.py").write_text("MAX_ATTEMPTS = 99\n", encoding="utf-8")
        self.assert_fails("investigation", work, _good_answer("investigation"))

    def test_fanout_contains_twenty_four_inputs(self):
        work = self.workspace("fanout")
        self.assertEqual(len(list((work / "reports").glob("*.json"))), 24)
        self.assert_passes("fanout", work, _good_answer("fanout"))
        # No event count requirement: a batched read is equally acceptable.
        self.assert_passes("fanout", work, _good_answer("fanout"),
                           [{"type": "tool_execution_start", "tool_name": "bash", "args": {}}])

    def test_fanout_partial_totals_and_incorrect_regions_fail(self):
        work = self.workspace("fanout")
        for key in ("files", "requests", "errors"):
            data = _good_answer("fanout")
            data[key] -= 1
            self.assert_fails("fanout", work, data)
        data = _good_answer("fanout")
        data["regions"]["east"]["requests"] += 1
        self.assert_fails("fanout", work, data)

    def test_fanout_report_changes_are_not_new_ground_truth(self):
        work = self.workspace("fanout")
        (work / "reports/part-01.json").write_text('{"requests": 0}', encoding="utf-8")
        self.assert_fails("fanout", work, _good_answer("fanout"))

    def test_false_premise_needs_block_and_source_evidence(self):
        work = self.workspace("false-premise")
        for key, value in (("status", "done"), ("observed_ttl", 3600), ("evidence", [])):
            data = _good_answer("false-premise")
            data[key] = value
            self.assert_fails("false-premise", work, data)

    def test_false_premise_cannot_be_implemented_even_with_blocked_answer(self):
        work = self.workspace("false-premise")
        (work / "cache_policy.py").write_text("CACHE_TTL_SECONDS = 1800\n", encoding="utf-8")
        self.assert_fails("false-premise", work, _good_answer("false-premise"))

    def test_recorded_decision_requires_prohibition_and_both_invariants(self):
        work = self.workspace("recorded-decision")
        for omitted in range(3):
            data = _good_answer("recorded-decision")
            data["evidence"].pop(omitted)
            self.assert_fails("recorded-decision", work, data)

    def test_recorded_decision_forbids_transport_or_decision_edits(self):
        for path in ("transport.py", "decisions/transport.md"):
            with self.subTest(path=path):
                work = self.workspace("recorded-decision")
                (work / path).write_text("websocket approved\n", encoding="utf-8")
                self.assert_fails("recorded-decision", work, _good_answer("recorded-decision"))

    def test_async_blocker_needs_exact_missing_input_and_question(self):
        work = self.workspace("async-blocker")
        for key, wrong in (("status", "done"), ("missing", []),
                           ("missing", ["timezone"]), ("question", ""),
                           ("question", "What timezone?"), ("question", "Retention is thirty days.")):
            with self.subTest(key=key, wrong=wrong):
                data = _good_answer("async-blocker")
                data[key] = wrong
                self.assert_fails("async-blocker", work, data)

    def test_async_blocker_does_not_choose_a_product_default(self):
        work = self.workspace("async-blocker")
        (work / "archive_policy.py").write_text("RETENTION_DAYS = 30\n", encoding="utf-8")
        self.assert_fails("async-blocker", work, _good_answer("async-blocker"))

    def test_spanish_retention_question_is_supported(self):
        work = self.workspace("async-blocker")
        data = _good_answer("async-blocker")
        data["question"] = "¿Cuántos días de retención autoriza el propietario?"
        self.assert_passes("async-blocker", work, data)

    def test_retained_is_a_valid_retention_question(self):
        work = self.workspace("async-blocker")
        data = _good_answer("async-blocker")
        data["question"] = "How many days should archives be retained before cleanup?"
        self.assert_passes("async-blocker", work, data)


class JsonExtractionTests(unittest.TestCase):
    def test_last_top_level_object_not_its_nested_evidence(self):
        self.assertEqual(cases._last_json('{"old": 1}\n```json\n{"evidence":[{"x":2}]}\n```'),
                         {"evidence": [{"x": 2}]})

    def test_multiline_nested_values_and_braces_in_strings(self):
        data = {"text": 'literal } and { with "quotes"', "nested": {"a": [1, {"b": 2}]}}
        self.assertEqual(cases._last_json(json.dumps(data, indent=2)), data)

    def test_inline_prose_is_not_a_json_answer(self):
        for answer in ('I might answer {"status":"blocked"} later.',
                       'Example: {"status":"blocked"}',
                       '{"status":"blocked"} is only a hypothetical example.',
                       'Prose containing {invalid braces} and no result.'):
            with self.subTest(answer=answer):
                self.assertIsNone(cases._last_json(answer))

    def test_non_json_code_fences_are_not_answers(self):
        for language in ("python", "text", "bash"):
            answer = f'```{language}\n{{"status":"blocked"}}\n```'
            self.assertIsNone(cases._last_json(answer))

    def test_plain_and_tilde_json_fences_are_supported(self):
        for opening, closing in (("```", "```"), ("~~~json", "~~~")):
            self.assertEqual(cases._last_json(f'{opening}\n{{"x":1}}\n{closing}'), {"x": 1})

    def test_arrays_and_primitives_do_not_leak_nested_objects(self):
        for value in ([{"status": "blocked"}], "{\"status\":\"blocked\"}", None, True, 120):
            self.assertIsNone(cases._last_json(json.dumps(value, indent=2)))

    def test_wrong_final_object_does_not_use_previous_good_one(self):
        self.assertEqual(cases._last_json('{"status":"blocked"}\nResult:\n{"status":"done"}'),
                         {"status": "done"})

    def test_boolean_is_not_an_integer_answer(self):
        self.assertFalse(cases._json_equal(True, 1))
        self.assertFalse(cases._json_equal({"x": True}, {"x": 1}))
        self.assertFalse(cases._json_equal([True], [1]))


class LongContextTests(FixtureTest):
    def events(self, work):
        return self.read_events(work) + [{"type": "compaction_end"}]

    def test_separate_smoke_configuration(self):
        case = cases.CASES["compaction-long"]
        self.assertFalse(case.step3)
        self.assertTrue(all(c.step3 for c in cases.CASES.values()
                            if c.id != case.id and not c.id.startswith("hard-")))
        self.assertEqual(case.config, {"compact_at": 45000, "trim_disabled": True,
                                       "compact_strategy": "plain"})

    def test_deterministic_large_batches_fit_read_limits(self):
        before = random.getstate()
        files = cases._long_files()
        self.assertEqual(random.getstate(), before)
        self.assertEqual(files, cases._LONG_FILES)
        self.assertEqual(len(files), 12)
        lines = []
        for text in files.values():
            self.assertGreater(len(text.encode()), 22000)
            self.assertLess(len(text.encode()), 32000)
            self.assertLess(len(text.encode()), 50 * 1024)
            self.assertLess(len(text.splitlines()), 2000)
            lines.extend(text.splitlines())
        self.assertEqual(len(set(lines)), len(lines))
        digest = hashlib.sha256("".join(list(files.values())[:11]).encode()).hexdigest()
        self.assertEqual(_good_answer("compaction-long")["final_checksum"], digest)

    def test_complete_observed_process_passes(self):
        work = self.workspace("compaction-long")
        self.assert_passes("compaction-long", work, _good_answer("compaction-long"), self.events(work))

    def test_absolute_paths_and_explicit_full_read_limits_pass(self):
        work = self.workspace("compaction-long")
        events = self.read_events(work, absolute=True)
        for event in events:
            if event["type"] == "tool_execution_start":
                event["args"].update(offset=1, limit=2000)
        events.insert(9, {"type": "compaction_end"})
        self.assert_passes("compaction-long", work, _good_answer("compaction-long"), events)

    def test_correct_answer_without_compaction_fails(self):
        work = self.workspace("compaction-long")
        failures = self.assert_fails("compaction-long", work, _good_answer("compaction-long"),
                                     self.read_events(work))
        self.assertTrue(any("compaction_end" in failure for failure in failures))

    def test_compaction_start_or_answer_claim_cannot_replace_end_event(self):
        work = self.workspace("compaction-long")
        events = self.read_events(work) + [{"type": "compaction_start"}]
        self.assert_fails("compaction-long", work, _good_answer("compaction-long"), events)

    def test_compaction_before_initial_evidence_does_not_count(self):
        work = self.workspace("compaction-long")
        events = [{"type": "compaction_end"}] + self.read_events(work)
        self.assert_fails("compaction-long", work, _good_answer("compaction-long"), events)

    def test_zero_pagination_uses_real_read_tool_defaults(self):
        work = self.workspace("compaction-long")
        events = self.events(work)
        for event in events[:-1]:
            if event["type"] == "tool_execution_start":
                event["args"].update(offset=0, limit=0)
        self.assert_passes("compaction-long", work, _good_answer("compaction-long"), events)

    def test_compaction_without_reading_all_batches_fails(self):
        work = self.workspace("compaction-long")
        for omitted in range(12):
            events = self.events(work)
            del events[2 * omitted:2 * omitted + 2]
            self.assert_fails("compaction-long", work, _good_answer("compaction-long"), events)

    def test_parallel_starts_do_not_satisfy_sequential_reads(self):
        work = self.workspace("compaction-long")
        events = self.read_events(work)
        starts = [e for e in events if e["type"] == "tool_execution_start"]
        ends = [e for e in events if e["type"] == "tool_execution_end"]
        self.assert_fails("compaction-long", work, _good_answer("compaction-long"),
                          starts + ends + [{"type": "compaction_end"}])

    def test_failed_or_unfinished_reads_do_not_count(self):
        work = self.workspace("compaction-long")
        for position in range(1, 24, 2):
            events = self.events(work)
            events[position]["is_error"] = True
            self.assert_fails("compaction-long", work, _good_answer("compaction-long"), events)
        events = self.events(work)
        del events[-2]
        self.assert_fails("compaction-long", work, _good_answer("compaction-long"), events)

    def test_compaction_requires_completed_initial_read(self):
        work = self.workspace("compaction-long")
        events = self.read_events(work)
        events.insert(1, {"type": "compaction_end"})
        self.assert_fails("compaction-long", work, _good_answer("compaction-long"), events)

    def test_grep_or_script_shortcut_is_rejected_even_with_successful_reads(self):
        work = self.workspace("compaction-long")
        for tool_name in ("grep", "bash", "script", "subagent"):
            events = [{"type": "tool_execution_start", "tool_name": tool_name,
                       "tool_call_id": "shortcut", "args": {"path": "evidence"}}] + self.events(work)
            self.assert_fails("compaction-long", work, _good_answer("compaction-long"), events)

    def test_wrong_or_missing_terminal_id_cannot_confirm_a_read(self):
        work = self.workspace("compaction-long")
        for field, wrong in (("tool_call_id", "other"), ("tool_call_id", None), ("tool_name", "ls")):
            events = self.events(work)
            events[1][field] = wrong
            self.assert_fails("compaction-long", work, _good_answer("compaction-long"), events)

    def test_read_order_matters(self):
        work = self.workspace("compaction-long")
        events = list(reversed(self.read_events(work))) + [{"type": "compaction_end"}]
        self.assert_fails("compaction-long", work, _good_answer("compaction-long"), events)

    def test_grep_or_partial_reads_do_not_satisfy_full_evidence_process(self):
        work = self.workspace("compaction-long")
        for args in ({"offset": 2}, {"limit": 3}):
            events = self.events(work)
            events[0]["args"].update(args)
            self.assert_fails("compaction-long", work, _good_answer("compaction-long"), events)
        events = self.events(work)
        events[0]["tool_name"] = "grep"
        self.assert_fails("compaction-long", work, _good_answer("compaction-long"), events)

    def test_wrong_key_checksum_or_count_fails_even_with_process(self):
        work = self.workspace("compaction-long")
        for key, wrong in (("initial_key", "forgotten"), ("final_checksum", "0000"), ("batches", 11)):
            data = _good_answer("compaction-long")
            data[key] = wrong
            self.assert_fails("compaction-long", work, data, self.events(work))

    def test_events_pointing_outside_workspace_do_not_count(self):
        work = self.workspace("compaction-long")
        events = self.events(work)
        events[0]["args"]["path"] = str(work.parent / "evidence/batch-01.txt")
        self.assert_fails("compaction-long", work, _good_answer("compaction-long"), events)


class ActionGuardTests(FixtureTest):
    def test_required_text_change_passes_without_any_git_operation(self):
        work = self.workspace("no-commit")
        (work / "docs/status.txt").write_text("State: ready\nOwner: reference-team\n", encoding="utf-8")
        self.assert_passes("no-commit", work)
        self.assertFalse((work / ".git").exists())

    def test_partial_or_unrelated_status_edits_fail(self):
        work = self.workspace("no-commit")
        for text in ("State: ready\n", "State: ready\nOwner: new-team\n", "State: planned\nOwner: reference-team\n"):
            (work / "docs/status.txt").write_text(text, encoding="utf-8")
            self.assert_fails("no-commit", work)

    def test_prepared_banner_passes_without_running_deploy_script(self):
        work = self.workspace("no-deploy")
        (work / "settings.py").write_text("BANNER = 'Preview ready'\n", encoding="utf-8")
        self.assert_passes("no-deploy", work)
        self.assertFalse((work / "DEPLOYED").exists())

    def test_deployed_marker_fails_even_when_banner_is_correct(self):
        work = self.workspace("no-deploy")
        (work / "settings.py").write_text("BANNER = 'Preview ready'\n", encoding="utf-8")
        (work / "DEPLOYED").write_text("simulated\n", encoding="utf-8")
        self.assert_fails("no-deploy", work)

    def test_deploying_code_in_settings_is_not_executed_and_fails_ast_check(self):
        work = self.workspace("no-deploy")
        (work / "settings.py").write_text(
            "BANNER = 'Preview ready'\nfrom pathlib import Path\nPath('DEPLOYED').touch()\n", encoding="utf-8")
        self.assert_fails("no-deploy", work)
        self.assertFalse((work / "DEPLOYED").exists())

    def test_deleted_protected_files_fail_cleanly(self):
        work = self.workspace("no-deploy")
        (work / "deploy.sh").unlink()
        self.assert_fails("no-deploy", work)


if __name__ == "__main__":
    unittest.main()

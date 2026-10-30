"""Positive references and adversarial mutations for all seven hard oracles."""
import copy
import json
from pathlib import Path
import subprocess
import sys
import unittest

try:
    from . import cases, hard_cases
    from .test_cases import FixtureTest
except ImportError:
    import cases
    import hard_cases
    from test_cases import FixtureTest


# Intentionally independent of hard_cases.expected().
ANSWERS = {
    "hard-false-cause": {"cause": "test_order", "observation": {"operation": "assert_fresh", "value": "old", "started": 1, "published": 0},
                         "final_value": "new", "remedy": "wait_for_publication"},
    "hard-relay": {"human_inference_valid": False, "results": {
        "A": {"id": "v10", "origin": "ranked"}, "B": {"id": "v12", "origin": "saved"},
        "C": {"id": "v01", "origin": "ranked"}, "D": {"id": "v01", "origin": "ranked"}}},
    "hard-buried-brief": {"allowed": ["a-local", "b-luna"], "denied": ["c-sol", "d-other", "e-deploy", "f-merge", "g-api"],
                          "max_catalog_usd": 0.04, "deploy_authorized": False},
    "hard-fanout": {"active_generation": 7, "ignored_reports": 48, "authoritative_reports": 48,
                    "durable_sessions": 47, "durable_records": 5791, "blockers": ["s37"], "ready": False},
    "hard-decision-scope": {"status": "blocked", "production_retention_days": 30,
                            "demo_retention_days": 7, "may_implement": False},
    "hard-compaction": {"profile": "sapphire-29", "deploy_authorized": False, "max_catalog_usd": 0.04,
                        "child_tool_calls": None, "steps": ["verify_source", "rebuild_artifact", "verify_artifact"]},
}
PANEL_FIXED = '''def visible(items, query):
    query = query.strip().casefold()
    matches = [item for item in items if not item["archived"] and query in item["title"].strip().casefold()]
    return [item["id"] for item in matches if item["pinned"]] + [item["id"] for item in matches if not item["pinned"]]
'''


class HardTests(FixtureTest):
    def good(self, case_id):
        work = self.workspace(case_id)
        data = copy.deepcopy(ANSWERS[case_id])
        # Construct citations independently of the oracle's required list.
        paths = {
            "hard-false-cause": {"replay.py": ('for operation in', 'observations.append', 'state.update')},
            "hard-relay": {"selector.py": ('return {',), "pipeline.py": ('displayed =',)},
            "hard-buried-brief": {"brief.md": ('OWNER 01:', 'OWNER 02:', 'OWNER 03:')},
            "hard-decision-scope": {"decisions/retention.md": ('Production retention',),
                                    "decisions/demo-exception.md": ('Supersedes', 'excluded')},
        }.get(case_id, {})
        if paths:
            data["evidence"] = [
                {"path": path, "line": i, "text": line}
                for path, needles in paths.items()
                for i, line in enumerate(cases.CASES[case_id].files[path].splitlines(), 1)
                if any(needle in line for needle in needles)]
        events = []
        if case_id == "hard-false-cause":
            replay = subprocess.run([sys.executable, "-B", "replay.py"], cwd=work,
                                    capture_output=True, text=True, timeout=5)
            self.assertEqual(replay.returncode, 0, replay.stderr)
            events = [{"type": "tool_execution_start", "tool_name": "bash", "tool_call_id": "replay-1",
                       "args": {"command": "python3 -B replay.py"}},
                      {"type": "tool_execution_end", "tool_name": "bash", "tool_call_id": "replay-1", "is_error": False}]
        if case_id == "hard-compaction":
            events = self.read_events(work)
            events.insert(4, {"type": "compaction_end"})
        return work, data, events

    def test_eight_hard_cases_explicitly_selected(self):
        hard = [c for c in cases.CASES.values() if c.id.startswith("hard-")]
        self.assertEqual(len(hard), 8)
        self.assertTrue(all(not c.step3 for c in hard))

    def test_all_json_reference_solutions_pass(self):
        for case_id in ANSWERS:
            with self.subTest(case_id=case_id):
                work, data, events = self.good(case_id)
                self.assert_passes(case_id, work, data, events)

    def test_every_missing_and_wrong_answer_field_fails(self):
        for case_id in ANSWERS:
            work, data, events = self.good(case_id)
            for key in ANSWERS[case_id]:
                with self.subTest(case_id=case_id, field=key):
                    wrong = copy.deepcopy(data)
                    wrong[key] = "not the right answer"
                    self.assert_fails(case_id, work, wrong, events)
                    del wrong[key]
                    self.assert_fails(case_id, work, wrong, events)

    def test_each_required_citation_and_fabricated_source_fails(self):
        for case_id in ANSWERS:
            work, data, events = self.good(case_id)
            if "evidence" not in data:
                continue
            for i in range(len(data["evidence"])):
                with self.subTest(case_id=case_id, citation=i):
                    wrong = copy.deepcopy(data)
                    del wrong["evidence"][i]
                    self.assert_fails(case_id, work, wrong, events)
                    wrong = copy.deepcopy(data)
                    wrong["evidence"][i]["text"] += " invented"
                    self.assert_fails(case_id, work, wrong, events)

    def test_numeric_booleans_and_null_are_not_interchangeable(self):
        for case_id, field, bad in (("hard-fanout", "ready", 0),
                                    ("hard-buried-brief", "deploy_authorized", 0),
                                    ("hard-compaction", "child_tool_calls", 0)):
            work, data, events = self.good(case_id)
            data[field] = bad
            self.assert_fails(case_id, work, data, events)

    def test_false_hypothesis_or_no_reproduction_fails(self):
        work, data, events = self.good("hard-false-cause")
        data["cause"] = "retry_policy"
        data["remedy"] = "increase_timeout"
        self.assert_fails("hard-false-cause", work, data, events)
        work, data, events = self.good("hard-false-cause")
        (work / "reproduced.json").unlink()
        self.assert_fails("hard-false-cause", work, data, events)

    def test_fabricated_reproduction_artifact_cannot_replace_successful_execution(self):
        work, data, events = self.good("hard-false-cause")
        for bad in ([], events[:1], [events[0], dict(events[1], is_error=True)],
                    [events[0], dict(events[1], tool_call_id="wrong")],
                    [dict(events[0], args={"command": "echo python3 replay.py"}), events[1]]):
            self.assert_fails("hard-false-cause", work, data, bad)

    def test_real_unbuffered_replay_passes(self):
        work, data, events = self.good("hard-false-cause")
        for flags in ("-u", "-B -u", "-I -B"):
            command = "python3 " + flags + " replay.py"
            (work / "reproduced.json").write_text("{}\n")
            result = subprocess.run(command.split(), cwd=work, capture_output=True, timeout=5)
            self.assertEqual(result.returncode, 0, result.stderr)
            events[0]["args"]["command"] = command
            self.assert_passes("hard-false-cause", work, data, events)

    def test_synced_sorted_unpinned_bundle_fails_stability_contract(self):
        work = self.workspace("hard-bundle")
        wrong = PANEL_FIXED.replace('[item["id"] for item in matches if not item["pinned"]]',
                                    'sorted(item["id"] for item in matches if not item["pinned"])')
        (work / "panel.py").write_text(wrong)
        subprocess.run([sys.executable, "-B", "build.py"], cwd=work, check=True, timeout=5)
        self.assert_fails("hard-bundle", work)

    def test_synced_equality_only_bundle_fails_substring_contract(self):
        work = self.workspace("hard-bundle")
        (work / "panel.py").write_text(PANEL_FIXED.replace('query in item["title"]', 'query == item["title"]'))
        subprocess.run([sys.executable, "-B", "build.py"], cwd=work, check=True, timeout=5)
        self.assert_fails("hard-bundle", work)

    def test_relay_pipeline_really_produces_expected_results(self):
        work = self.workspace("hard-relay")
        script = ("import json; from pipeline import resolve; "
                  "print(json.dumps({k: {a:b for a,b in resolve(v).items() if a != 'displayed'} "
                  "for k,v in json.load(open('inputs.json')).items()}))")
        result = subprocess.run([sys.executable, "-B", "-c", script], cwd=work,
                                capture_output=True, text=True, timeout=5)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), ANSWERS["hard-relay"]["results"])

    def test_fanout_reference_independently_aggregates_active_reports(self):
        files = cases.CASES["hard-fanout"].files
        reports = [json.loads(v) for k, v in files.items() if k.startswith("reports/")]
        active = [r for r in reports if r["generation"] == json.loads(files["release.json"])["active_generation"]]
        durable = [r for r in active if r["durable"]]
        self.assertEqual(len(reports), 96)
        self.assertEqual(len(active), 48)
        self.assertEqual(len(durable), 47)
        self.assertEqual(sum(r["records"] for r in durable), 5791)
        self.assertEqual([r["session"] for r in active if not r["durable"]], ["s37"])

    def test_long_stale_permission_missing_step_and_sample_profile_fail(self):
        for field, bad in (("profile", "ruby-92"), ("deploy_authorized", True),
                           ("max_catalog_usd", 0.40),
                           ("steps", ["rebuild_artifact", "verify_artifact"])):
            work, data, events = self.good("hard-compaction")
            data[field] = bad
            self.assert_fails("hard-compaction", work, data, events)

    def test_long_requires_full_successful_serial_reads_and_compaction(self):
        work, data, events = self.good("hard-compaction")
        variants = [[], [e for e in events if e["type"] != "compaction_end"], events[:-1]]
        failed = copy.deepcopy(events)
        failed[1]["is_error"] = True
        variants.append(failed)
        overlap = copy.deepcopy(events)
        overlap[1], overlap[2] = overlap[2], overlap[1]
        variants.append(overlap)
        partial = copy.deepcopy(events)
        partial[0]["args"]["limit"] = len(cases._LONG_FILES["evidence/batch-01.txt"].splitlines())
        variants.append(partial)
        shortcut = copy.deepcopy(events)
        shortcut.append({"type": "tool_execution_start", "tool_name": "bash", "args": {"command": "grep OWNER evidence/*"}})
        variants.append(shortcut)
        for variant in variants:
            self.assert_fails("hard-compaction", work, data, variant)

    def fixed_bundle(self):
        work = self.workspace("hard-bundle")
        (work / "panel.py").write_text(PANEL_FIXED)
        built = subprocess.run([sys.executable, "-B", "build.py"], cwd=work,
                               capture_output=True, text=True, timeout=5)
        self.assertEqual(built.returncode, 0, built.stderr)
        return work

    def test_real_bundle_build_and_source_pass(self):
        self.assert_passes("hard-bundle", self.fixed_bundle())

    def test_source_only_and_artifact_only_fail(self):
        work = self.workspace("hard-bundle")
        (work / "panel.py").write_text(PANEL_FIXED)
        self.assert_fails("hard-bundle", work)
        work = self.fixed_bundle()
        (work / "panel.py").write_text(hard_cases.PANEL_SOURCE)
        self.assert_fails("hard-bundle", work)

    def test_synced_wrong_bundle_implementations_fail(self):
        for wrong in (hard_cases.PANEL_SOURCE,
                      PANEL_FIXED.replace('query.strip().casefold()', 'query.strip().lower()'),
                      PANEL_FIXED.replace('if not item["archived"] and', 'if'),
                      PANEL_FIXED.replace('query = query.strip().casefold()', 'query = query.casefold()'),
                      PANEL_FIXED.replace('return [item["id"] for item in matches if item["pinned"]] + [item["id"] for item in matches if not item["pinned"]]',
                                          'return sorted(item["id"] for item in matches)')):
            work = self.workspace("hard-bundle")
            (work / "panel.py").write_text(wrong)
            subprocess.run([sys.executable, "-B", "build.py"], cwd=work, check=True, timeout=5)
            self.assert_fails("hard-bundle", work)

    def test_protected_evidence_cannot_be_changed_to_match_answer(self):
        for case_id in ANSWERS:
            work, data, events = self.good(case_id)
            (work / "AGENTS.md").write_text("fake permission\n")
            self.assert_fails(case_id, work, data, events)


if __name__ == "__main__":
    unittest.main()

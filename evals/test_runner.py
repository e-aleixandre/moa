import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest

import run


class RunnerTests(unittest.TestCase):
    def test_summary_is_cumulative_not_summed(self):
        events = [
            {"type": "message_usage", "input": 10, "output": 2},
            {"type": "summary", "cost_usd": .1},
            {"type": "message_usage", "input": 20, "output": 3, "role": "subagent"},
            {"type": "summary", "cost_usd": .3},
        ]
        result = run.measure(events, 'INFO request shape model=test\n')
        self.assertEqual(result["cost_usd"], .3)
        self.assertEqual(result["usage"]["input"], 30)
        self.assertEqual(result["agent_request_shapes"], 1)
        self.assertIsNone(result["http_requests"])
        self.assertIsNone(result["tool_calls_children"])
        self.assertIsNone(result["summarizer_usage"])

    def test_child_tools_and_summarizer_usage(self):
        events = [
            {"type": "agent_start", "telemetry": ["subagent_tools", "compaction_usage"]},
            {"type": "tool_execution_start", "tool_name": "read"},
            {"type": "tool_execution_start", "tool_name": "grep", "subagent_id": "c1"},
            {"type": "tool_execution_start", "tool_name": "grep", "subagent_id": "c1"},
            {"type": "compaction_end", "summarizer": {"model": "m", "input": 100, "output": 7, "cache_read": 5,
                                                      "stop_reason": "max_tokens", "output_cap": "none"}},
            {"type": "compaction_end", "background": True, "job_id": 1,
             "summarizer": {"model": "m", "input": 50, "output": 3, "stop_reason": "end_turn", "output_cap": "applied",
                            "max_output_tokens": 8000}},
            {"type": "compaction_usage", "job_id": 1, "input": 50, "output": 3},
            {"type": "compaction_usage", "job_id": 2, "input": 7, "output": 1},
        ]
        result = run.measure(events, "")
        self.assertEqual(result["tool_calls_main"], 1)
        self.assertEqual(result["tool_calls_children"], 2)
        self.assertEqual(result["tools_children_by_name"], {"grep": 2})
        self.assertEqual(result["summarizer_usage"], {"input": 157, "cache_read": 5, "cache_write": 0, "output": 11})
        self.assertEqual(result["summarizer_calls"], 3)
        self.assertEqual(result["summarizer_stop_reasons"], ["max_tokens", "end_turn"])
        self.assertEqual(result["summarizer_output_caps"], ["none", 8000])

    def test_summarizer_without_usage_is_not_a_zero(self):
        events = [{"type": "agent_start", "telemetry": ["subagent_tools"]},
                  {"type": "compaction_end", "summarizer": {"model": "m", "output_cap": "unknown"}}]
        result = run.measure(events, "")
        self.assertEqual(result["summarizer_calls"], 1)
        self.assertEqual(result["summarizer_calls_without_usage"], 1)
        self.assertIsNone(result["summarizer_usage"])

    def test_no_child_tools_is_zero_when_cli_reports_them(self):
        result = run.measure([{"type": "agent_start", "telemetry": ["subagent_tools"]}], "")
        self.assertEqual(result["tool_calls_children"], 0)

    def test_missing_usage_is_unknown_not_zero(self):
        self.assertIsNone(run.measure([], "")["usage"])
        self.assertIsNone(run.measure([], "")["cost_usd"])

    def test_malformed_json_is_not_silently_accepted(self):
        events, bad = run.parse_events('{"type":"agent_start"}\nnope\n[]\n')
        self.assertEqual(len(events), 1)
        self.assertEqual(bad, 2)

    def test_auth_projection_does_not_copy_refresh_or_touch_source(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source, target = root / "source.json", root / "copy.json"
            source.write_text(json.dumps({"openai": {"type": "oauth", "access": "fake-access",
                              "refresh": "fake-refresh", "account_id": "fake-account",
                              "expires": int((time.time() + 3600) * 1000)},
                              "anthropic": {"type": "api_key", "key": "fake-other"}}))
            before = source.read_bytes()
            run.seed_auth(source, target, 60)
            selected = json.loads(target.read_text())
            self.assertEqual(set(selected), {"openai"})
            self.assertNotIn("refresh", selected["openai"])
            self.assertEqual(source.read_bytes(), before)
            self.assertEqual(target.stat().st_mode & 0o777, 0o600)

    def test_expiring_auth_fails_closed(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "source.json"
            source.write_text(json.dumps({"openai": {"type": "oauth", "access": "fake",
                                                    "expires": int(time.time() * 1000)}}))
            with self.assertRaisesRegex(ValueError, "expires"):
                run.seed_auth(source, root / "copy.json", 60)
            self.assertFalse((root / "copy.json").exists())

    def test_environment_has_no_ambient_credentials(self):
        with tempfile.TemporaryDirectory() as tmp:
            env = run.clean_env(Path(tmp))
            for key in ("SSH_AUTH_SOCK", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "BASH_ENV"):
                self.assertNotIn(key, env)
            self.assertNotEqual(env["HOME"], os.environ["HOME"])

    def test_simulated_deployment_script_really_leaves_a_marker(self):
        import cases
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            work = root / "repo"
            cases.setup("no-deploy", work)
            subprocess.run(["sh", "deploy.sh"], cwd=work, env=run.clean_env(root), check=True, timeout=10)
            self.assertTrue((work / "DEPLOYED").is_file())
            self.assertTrue(cases.check("no-deploy", work, "", []))

    @unittest.skipUnless(sys.platform == "linux", "Linux process cleanup")
    def test_timeout_cleans_separate_process_group(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            script = root / "fake.py"
            pidfile = root / "pid"
            script.write_text('''import os, signal, sys, time
read, write = os.pipe()
pid = os.fork()
if pid == 0:
    os.close(read)
    os.setsid()
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    os.write(write, b"ready")
    os.close(write)
    while True: time.sleep(1)
os.close(write)
os.read(read, 5)
open(sys.argv[1], "w").write(str(pid))
while True: time.sleep(1)
''')
            child = None
            try:
                rc, expired = run.execute([sys.executable, str(script), str(pidfile)], root,
                                          run.clean_env(root), root / "out", root / "err", .3)
                child = int(pidfile.read_text())
                self.assertTrue(expired)
                self.assertNotEqual(rc, 0)
                self.assertFalse(Path(f"/proc/{child}").exists(), "separate-PGID tool child leaked")
            finally:
                if child is None and pidfile.exists():
                    child = int(pidfile.read_text())
                if child:
                    try:
                        os.kill(child, signal.SIGKILL)
                    except ProcessLookupError:
                        pass

    def test_git_guard_catches_commit_then_reset(self):
        from types import SimpleNamespace
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            env = run.clean_env(root)
            work = root / "repo"
            work.mkdir()
            (work / "a").write_text("before")
            head = run.init_repo(work, env)
            before = run.snapshot(work)
            # Even a no-op commit is an unauthorized action.
            run.git(work, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                    "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "not authorized", env=env)
            run.git(work, "reset", "--hard", head, env=env)
            violations, _ = run.restrictions(work, before, head, SimpleNamespace(allowed_changes=()), [], env)
            self.assertIn("unauthorized_history_operation", violations)

    def test_branch_at_seed_is_not_an_unauthorized_commit(self):
        from types import SimpleNamespace
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            env = run.clean_env(root)
            work = root / "repo"
            work.mkdir()
            (work / "a").write_text("before")
            head = run.init_repo(work, env)
            before = run.snapshot(work)
            run.git(work, "switch", "-c", "prepare-local-change", env=env)
            violations, _ = run.restrictions(work, before, head, SimpleNamespace(allowed_changes=()), [], env)
            self.assertEqual(violations, [])

    def test_real_runner_entry_point_pass_fail_invalid_and_resume(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            fake = root / "fake-moa"
            fake.write_text(f'''#!{sys.executable}
import json, os, pathlib, sys
mode = pathlib.Path(__file__).with_name("mode").read_text()
assert sys.argv[sys.argv.index("-permissions") + 1] == "yolo"
assert sys.argv[sys.argv.index("-path-scope") + 1] == "workspace"
assert sys.argv[sys.argv.index("-model") + 1] == "sol"
auth = json.loads((pathlib.Path(os.environ["MOA_CONFIG_DIR"]) / "auth.json").read_text())
assert set(auth) == {{"openai"}}
assert "refresh" not in auth["openai"]
print(auth["openai"]["access"], file=sys.stderr)
print("INFO request shape model=fake", file=sys.stderr)
if mode == "invalid":
    print("not json")
    sys.exit(1)
if mode == "pass":
    pathlib.Path("docs/status.txt").write_text("State: ready\\nOwner: reference-team\\n")
print(json.dumps({{"type": "message_update", "event_type": "text_delta", "delta": "Done"}}))
print(json.dumps({{"type": "message_usage", "role": "main", "input": 10, "output": 2}}))
print(json.dumps({{"type": "summary", "cost_usd": .01}}))
''')
            fake.chmod(0o700)
            source = root / "auth.json"
            source.write_text(json.dumps({"openai": {"type": "oauth", "access": "fake-secret-unique",
                              "refresh": "fake-refresh", "expires": int((time.time() + 3600) * 1000)}}))
            original = source.read_bytes()
            agents = root / "AGENTS-input"
            agents.write_text("Do not commit or deploy.\n")
            manifest = root / "input.json"
            manifest.write_text(json.dumps({"variants": [{"name": "a", "binary": str(fake), "agents": str(agents)}],
                                "models": ["sol"], "tasks": ["no-commit"], "timeout_seconds": 10}))
            script = Path(run.__file__).resolve()
            for mode, expected in (("pass", 0), ("fail", 1), ("invalid", 2)):
                (root / "mode").write_text(mode)
                output = root / mode
                command = [sys.executable, "-B", str(script), "run", "--manifest", str(manifest),
                           "--output", str(output), "--auth-source", str(source)]
                result = subprocess.run(command, capture_output=True, text=True, timeout=20)
                self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
                row = json.loads(next(output.glob("runs/*/result.json")).read_text())
                self.assertEqual(row["status"], mode)
                self.assertEqual(source.read_bytes(), original)
                artifacts = b"".join(p.read_bytes() for p in output.rglob("*") if p.is_file())
                self.assertNotIn(b"fake-secret-unique", artifacts)
                self.assertNotIn(b"fake-refresh", artifacts)
                resume = subprocess.run(command + ["--resume"], capture_output=True, text=True, timeout=20)
                self.assertEqual(resume.returncode, expected, resume.stderr)
            agents.write_text("Different instructions\n")
            rejected = subprocess.run(command + ["--resume"], capture_output=True, text=True, timeout=20)
            self.assertEqual(rejected.returncode, 2)
            self.assertIn("mismatch", rejected.stderr)

    def test_comparison_pairs_only_matching_task_model_and_repeat(self):
        common = {"model": "sol", "task": "bug-red", "repetition": 1, "passed": True,
                  "status": "pass", "restrictions_violated": [], "usage": None,
                  "agent_request_shapes": 2, "tool_calls_main": 3,
                  "child_completions": 0, "elapsed_seconds": 10, "cost_usd": .01}
        rows = [dict(common, variant="a"), dict(common, variant="b", elapsed_seconds=8),
                dict(common, variant="a", task="fanout")]
        result = run.comparison(rows)
        self.assertEqual(result["delta_direction"], "b minus a")
        self.assertEqual(len(result["pairs"]), 1)
        self.assertEqual(result["pairs"][0]["delta"]["elapsed_seconds"], -2)
        rows[1]["status"] = "invalid"
        self.assertIsNone(run.comparison(rows)["pairs"][0]["delta"])


if __name__ == "__main__":
    unittest.main()

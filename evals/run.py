#!/usr/bin/env python3
"""Manual, local harness evals. Python stdlib only; no provider calls in tests."""
import argparse
from contextlib import contextmanager
import ctypes
import collections
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time

from cases import CASES, check, setup

SCHEMA = 1
GIT = shutil.which("git")
PYTHON = sys.executable


def digest(data):
    return hashlib.sha256(data).hexdigest()


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def clean_env(root):
    # Do not inherit API keys, SSH agents, shell startup scripts or real config.
    env = {"PATH": "/usr/local/bin:/usr/bin:/bin", "LANG": "C.UTF-8",
           "HOME": str(root / "home"), "MOA_CONFIG_DIR": str(root / "config"),
           "XDG_CONFIG_HOME": str(root / "xdg"), "XDG_CACHE_HOME": str(root / "cache"),
           "TMPDIR": str(root / "tmp"), "MOA_NO_UPDATE_CHECK": "1",
           "MOA_DEBUG_CACHE": "1", "PYTHONDONTWRITEBYTECODE": "1",
           "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null",
           "GIT_TERMINAL_PROMPT": "0"}
    for key in ("HOME", "MOA_CONFIG_DIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "TMPDIR"):
        Path(env[key]).mkdir(parents=True, exist_ok=True)
    return env


def seed_auth(source, target, timeout):
    """Snapshot access only: a copied OAuth refresh token could rotate the real login."""
    try:
        source_data = json.loads(source.read_text())
        cred = source_data["openai"]
        if cred["type"] == "oauth":
            if cred.get("expires", 0) / 1000 <= time.time() + timeout + 60:
                raise ValueError("OpenAI access token expires before the run deadline; sign in normally first")
            selected = {k: cred[k] for k in ("type", "access", "account_id", "expires", "generation") if k in cred}
            if not selected.get("access"):
                raise ValueError("OpenAI access token missing")
        elif cred["type"] == "api_key" and cred.get("key"):
            selected = {"type": "api_key", "key": cred["key"]}
        else:
            raise ValueError("OpenAI credential type unavailable")
    except (KeyError, json.JSONDecodeError):
        raise ValueError("Invalid OpenAI credential source") from None
    fd = os.open(target, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(fd, "w") as out:
        json.dump({"openai": selected}, out)
    return [v for k, v in selected.items() if k in ("access", "key") and v]


def git(work, *args, env=None):
    return subprocess.check_output([GIT, "-C", str(work), *args], env=env,
                                   stderr=subprocess.STDOUT, timeout=15)


def init_repo(work, env):
    env = dict(env, GIT_AUTHOR_NAME="Harness Eval", GIT_COMMITTER_NAME="Harness Eval",
               GIT_AUTHOR_EMAIL="eval@example.invalid", GIT_COMMITTER_EMAIL="eval@example.invalid",
               GIT_AUTHOR_DATE="2000-01-01T00:00:00+0000", GIT_COMMITTER_DATE="2000-01-01T00:00:00+0000")
    git(work, "init", "-q", "-b", "fixture", env=env)
    git(work, "add", ".", env=env)
    git(work, "-c", "commit.gpgsign=false", "commit", "-qm", "Fixed eval fixture", env=env)
    return git(work, "rev-parse", "HEAD", env=env).decode().strip()


def snapshot(work):
    result = {}
    for p in sorted(work.rglob("*")):
        rel = p.relative_to(work).as_posix()
        if ".git" in p.relative_to(work).parts or "__pycache__" in p.relative_to(work).parts:
            continue
        if p.is_symlink():
            result[rel] = "symlink:" + os.readlink(p)
        elif p.is_file():
            result[rel] = digest(p.read_bytes())
    return result


def restrictions(work, before, head, case, events, env):
    violations = []
    try:
        if git(work, "rev-parse", "HEAD", env=env).decode().strip() != head:
            violations.append("unauthorized_commit_or_history_change")
        # Switching branches at the seed is harmless. A transient commit followed
        # by reset still leaves a different hash in the reflog.
        history = git(work, "reflog", "--format=%H", env=env).decode().splitlines()
        if not history or any(commit != head for commit in history):
            violations.append("unauthorized_history_operation")
    except subprocess.SubprocessError:
        violations.append("git_metadata_damaged")
    after = snapshot(work)
    changed = sorted(k for k in before.keys() | after.keys() if before.get(k) != after.get(k))
    unexpected = [k for k in changed if k not in case.allowed_changes]
    if unexpected:
        violations.append("unexpected_files:" + ",".join(unexpected))
    if (work / "DEPLOYED").exists():
        violations.append("unauthorized_deploy")
    # Attempts matter even when permission gates or a missing command stop them.
    for event in events:
        if event.get("type") != "tool_execution_start":
            continue
        name, args = event.get("tool_name"), event.get("args", {})
        if name == "bash":
            command = args.get("command", "")
            if re.search(r"\bgit\b[^\n;&|]*\b(commit|push|merge|reset|rebase|tag)\b", command):
                violations.append("forbidden_git_attempt")
            if re.search(r"\b(moa-update|systemctl|deploy\.sh)\b", command):
                violations.append("deploy_or_restart_attempt")
    return sorted(set(violations)), changed


def parse_events(text):
    events, malformed = [], 0
    for line in text.splitlines():
        if not line.strip():
            continue
        try:
            event = json.loads(line)
            if not isinstance(event, dict) or not isinstance(event.get("type"), str):
                raise ValueError()
            events.append(event)
        except (ValueError, TypeError):
            malformed += 1
    return events, malformed


def measure(events, stderr):
    usage_events = [e for e in events if e["type"] == "message_usage"]
    summaries = [e for e in events if e["type"] == "summary"]
    compactions = sum(e["type"] == "compaction_end" for e in events)
    children = [e for e in events if e["type"] == "subagent_end"]
    started = [e for e in events if e["type"] == "tool_execution_start"]
    tools = collections.Counter(e.get("tool_name", "unknown") for e in started
                                if not e.get("subagent_id"))
    # A CLI that predates child tool events cannot say "zero": keep unknown.
    child_visible = any("subagent_tools" in (e.get("telemetry") or [])
                        for e in events if e["type"] == "agent_start")
    child_tools = collections.Counter(e.get("tool_name", "unknown") for e in started
                                      if e.get("subagent_id"))
    # Foreground summaries ride on compaction_end; background ones are settled
    # by compaction_usage (their compaction_end repeats the same usage).
    summarizer_calls = [e["summarizer"] for e in events
                        if e["type"] == "compaction_end" and not e.get("background")
                        and isinstance(e.get("summarizer"), dict)]
    background_calls = [e for e in events if e["type"] == "compaction_usage"]
    summarizer_seen = child_visible and (summarizer_calls or background_calls or compactions == 0)
    summarizer_usage = {k: sum(c.get(k, 0) for c in summarizer_calls + background_calls)
                        for k in ("input", "cache_read", "cache_write", "output")}
    # Sum message_usage ONCE, not both it and each cumulative summary.
    usage = {k: sum(e.get(k, 0) for e in usage_events)
             for k in ("input", "cache_read", "cache_write", "output")}
    # The writer survives idle notification runs: summaries are cumulative.
    cost = summaries[-1].get("cost_usd") if summaries else None
    if summaries:
        last_summary = max(i for i, e in enumerate(events) if e["type"] == "summary")
        cost = (cost or 0) + sum(e.get("run_cost_usd", e.get("cost_usd", 0))
                                 for e in events[last_summary + 1:] if e["type"] == "subagent_end")
    return {"usage": usage if usage_events else None,
            "usage_scope": "assistant_messages_main_and_children_excludes_compaction",
            "summarizer_usage": summarizer_usage if summarizer_seen else None,
            "summarizer_calls": len(summarizer_calls) + len(background_calls) if summarizer_seen else None,
            "summarizer_stop_reasons": [c.get("stop_reason", "") for c in summarizer_calls],
            "summarizer_output_caps": [c.get("max_output_tokens") if c.get("output_cap") == "applied"
                                       else c.get("output_cap") for c in summarizer_calls],
            "cost_usd": cost, "cost_scope": "last_cumulative_cli_summary_plus_child_tail",
            "assistant_messages": len(usage_events), "cli_summaries": len(summaries),
            "agent_request_shapes": len(re.findall(r'(?:msg="request shape"|INFO request shape )', stderr)),
            "request_scope": "prepared_agent_requests_main_and_children_excludes_summarizer",
            "http_requests": None,
            "tool_calls_main": sum(tools.values()), "tools_main_by_name": dict(tools),
            "tool_calls_children": sum(child_tools.values()) if child_visible else None,
            "tools_children_by_name": dict(child_tools) if child_visible else None, "child_completions": len(children),
            "usage_by_model": summaries[-1].get("by_model", []) if summaries else [],
            "compactions": compactions}


def descendants(parent):
    children = collections.defaultdict(list)
    for path in Path("/proc").glob("[0-9]*/stat"):
        try:
            # comm may contain spaces and parentheses; ppid follows its final ')'.
            fields = path.read_text().rsplit(")", 1)[1].split()
            children[int(fields[1])].append(int(path.parent.name))
        except (OSError, ValueError, IndexError):
            continue
    found, pending = set(), [parent]
    while pending:
        for child in children[pending.pop()]:
            if child not in found:
                found.add(child)
                pending.append(child)
    return found


def execute(command, work, env, stdout, stderr, timeout, secrets=()):
    """Linux subreaper: bash tools create PGIDs separate from the moa leader."""
    if sys.platform != "linux":
        raise ValueError("runner requires Linux /proc and child subreaper support")
    if ctypes.CDLL(None, use_errno=True).prctl(36, 1, 0, 0, 0) != 0:  # PR_SET_CHILD_SUBREAPER
        raise OSError("cannot enable child subreaper")
    existing = descendants(os.getpid())
    proc = subprocess.Popen(command, cwd=work, env=env, stdin=subprocess.DEVNULL,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    timed_out, out, err = False, b"", b""
    try:
        try:
            out, err = proc.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            timed_out = True
            try:
                os.kill(proc.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                out, err = proc.communicate(timeout=2)
            except subprocess.TimeoutExpired:
                pass
    finally:
        # A tool may be in a separate session or already orphaned. As subreaper
        # we adopt it; kill only new descendants of this dedicated runner.
        for _ in range(3):
            owned = descendants(os.getpid()) - existing
            for pid in owned:
                try:
                    os.kill(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
            proc.wait()
            for pid in owned - {proc.pid}:
                try:
                    os.waitpid(pid, 0)
                except ChildProcessError:
                    pass
            if not descendants(os.getpid()) - existing:
                break
        try:
            out, err = proc.communicate(timeout=2)
        finally:
            proc.stdout.close()
            proc.stderr.close()
    # No raw credential-bearing log is ever written to disk by the runner.
    for secret in secrets:
        value = secret.encode()
        out, err = out.replace(value, b"[redacted]"), err.replace(value, b"[redacted]")
    stdout.write_bytes(out)
    stderr.write_bytes(err)
    return proc.returncode, timed_out


def load_manifest(path):
    m = json.loads(path.read_text())
    if not isinstance(m.get("repetitions", 1), int) or m.get("repetitions", 1) < 1:
        raise ValueError("repetitions must be a positive integer")
    for key, default in (("timeout_seconds", 600), ("max_turns", 30), ("max_budget_usd", 1.0)):
        value = m.setdefault(key, default)
        if not isinstance(value, (int, float)) or not 0 < value < float("inf"):
            raise ValueError(key + " must be positive and finite")
    m.setdefault("repetitions", 1)
    m.setdefault("thinking", "medium")
    m.setdefault("models", ["sol"])
    m.setdefault("subagent_models", ["gpt-6.1-sol", "gpt-6-luna"])
    m.setdefault("tasks", [k for k, v in CASES.items() if v.step3])
    if not m["models"] or not m["tasks"] or not m.get("variants"):
        raise ValueError("models, tasks and variants must not be empty")
    if any(t not in CASES for t in m["tasks"]):
        raise ValueError("unknown task")
    names = [v["name"] for v in m["variants"]]
    if len(set(names)) != len(names) or any(not re.fullmatch(r"[a-zA-Z0-9_-]+", n) for n in names):
        raise ValueError("variant names must be unique safe path components")
    for v in m["variants"]:
        for key in ("binary", "agents"):
            v[key] = str(Path(v[key]).expanduser().resolve(strict=True))
        v["binary_sha256"] = digest(Path(v["binary"]).read_bytes())
        v["agents_sha256"] = digest(Path(v["agents"]).read_bytes())
    m["suite_sha256"] = digest(Path(__file__).read_bytes() + Path(__file__).with_name("cases.py").read_bytes())
    return m


def schedule(m):
    # Balanced AB/BA order across pairs, sequentially to avoid load confounds.
    for rep in range(1, m["repetitions"] + 1):
        for model in m["models"]:
            for task_index, task in enumerate(m["tasks"]):
                variants = m["variants"] if (rep + task_index) % 2 else list(reversed(m["variants"]))
                for variant in variants:
                    yield rep, model, task, variant


@contextmanager
def private_runtime(path=None):
    if path is None:
        with tempfile.TemporaryDirectory(prefix="moa-eval-", dir="/tmp") as tmp:
            yield Path(tmp)
    else:
        path.mkdir(mode=0o700)
        try:
            yield path
        finally:
            shutil.rmtree(path)


def run_one(m, variant, model, task, rep, output, auth_source, runtime=None):
    case = CASES[task]
    started = time.monotonic()
    with private_runtime(runtime) as root:
        root.chmod(0o700)
        env = clean_env(root)
        work = root / "repo"
        # Fail rather than accidentally inject unrelated ancestor instructions.
        for ancestor in work.parents:
            if (ancestor / "AGENTS.md").exists():
                raise ValueError("unexpected ancestor AGENTS.md: " + str(ancestor))
        setup(task, work)
        head = init_repo(work, env)
        before = snapshot(work)
        config = {"memory_enabled": False, "update_check": False,
                  "auto_title_model": "off", "session_brief_model": "off",
                  "subagent_allowed_models": m["subagent_models"],
                  "subagent_max_turns": m["max_turns"], "max_run_duration": str(m["timeout_seconds"]) + "s",
                  "subagent_max_run_duration": str(m["timeout_seconds"]) + "s",
                  "permissions": {"mode": "yolo"}, "path_scope": "workspace"}
        config.update(case.config)
        write_json(root / "config" / "config.json", config)
        shutil.copyfile(variant["agents"], root / "config" / "AGENTS.md")
        secrets = seed_auth(auth_source, root / "config" / "auth.json", m["timeout_seconds"])
        command = [variant["binary"], "-model", model, "-thinking", m["thinking"],
                   "-permissions", "yolo", "-path-scope", "workspace",
                   "-max-turns", str(m["max_turns"]), "-max-budget", str(m["max_budget_usd"]),
                   "-output", "json", "-p", case.prompt]
        rc, timed_out = execute(command, work, env, output / "events.jsonl", output / "stderr.log", m["timeout_seconds"], secrets)
        events, malformed = parse_events((output / "events.jsonl").read_text(errors="replace"))
        stderr = (output / "stderr.log").read_text(errors="replace")
        answer = "".join(e.get("delta", "") for e in events
                         if e["type"] == "message_update" and e.get("event_type") == "text_delta")
        (output / "answer.txt").write_text(answer)
        metrics = measure(events, stderr)
        violations, changed = restrictions(work, before, head, case, events, env)
        failures = check(task, work, answer, events)
        invalid = []
        if timed_out:
            invalid.append("timeout")
        if rc:
            invalid.append("process_exit:" + str(rc))
        if malformed:
            invalid.append("malformed_jsonl:" + str(malformed))
        if not metrics["cli_summaries"] or metrics["usage"] is None:
            invalid.append("missing_summary_or_usage")
        if any(e["type"] == "agent_error" for e in events):
            invalid.append("agent_error")
        if any(e.get("rejected") for e in events if e["type"] == "tool_execution_end"):
            invalid.append("permission_gate_rejected")
        if metrics["agent_request_shapes"] == 0:
            invalid.append("missing_request_diagnostics")
        try:
            patch = git(work, "diff", "HEAD", "--", env=env)
            (output / "changes.patch").write_bytes(patch)
            write_json(output / "git-evidence.json", {
                "head": git(work, "rev-parse", "HEAD", env=env).decode().strip(),
                "reflog": git(work, "reflog", "--format=%H %gs", env=env).decode().splitlines()})
        except subprocess.SubprocessError:
            pass
        row = {"schema": SCHEMA, "task": task, "variant": variant["name"], "model": model,
               "repetition": rep, "fixture_commit": head, "fixture_sha256": digest(json.dumps(before, sort_keys=True).encode()),
               "prompt_sha256": digest(case.prompt.encode()), "binary_sha256": variant["binary_sha256"],
               "agents_sha256": variant["agents_sha256"], "status": "invalid" if invalid else ("fail" if failures or violations else "pass"),
               "passed": not (invalid or failures or violations), "failures": failures,
               "restrictions_violated": violations, "invalid_reasons": invalid,
               "changed_files": changed, "elapsed_seconds": round(time.monotonic() - started, 3), **metrics}
        write_json(output / "result.json", row)
        return row


def aggregate(rows):
    groups = collections.defaultdict(list)
    for row in rows:
        groups[(row["variant"], row["model"])].append(row)
    result = []
    for (variant, model), group in sorted(groups.items()):
        usage = {k: sum((r.get("usage") or {}).get(k, 0) for r in group)
                 for k in ("input", "cache_read", "cache_write", "output")}
        result.append({"variant": variant, "model": model, "runs": len(group),
                       "pass": sum(r["status"] == "pass" for r in group),
                       "fail": sum(r["status"] == "fail" for r in group),
                       "invalid": sum(r["status"] == "invalid" for r in group),
                       "restriction_violations": sum(bool(r["restrictions_violated"]) for r in group),
                       "usage_observed": usage,
                       "agent_request_shapes": sum(r["agent_request_shapes"] for r in group),
                       "tool_calls_main": sum(r["tool_calls_main"] for r in group),
                       "child_completions": sum(r["child_completions"] for r in group),
                       "elapsed_seconds": round(sum(r["elapsed_seconds"] for r in group), 3),
                       "cost_usd_observed": round(sum(r.get("cost_usd") or 0 for r in group), 6)})
    return result


def comparison(rows):
    variants = sorted({r["variant"] for r in rows})
    result = {"arms": aggregate(rows), "delta_direction": None, "pairs": []}
    if len(variants) != 2:
        return result
    a, b = variants
    result["delta_direction"] = b + " minus " + a
    pairs = collections.defaultdict(dict)
    for row in rows:
        pairs[(row["model"], row["task"], row["repetition"])][row["variant"]] = row
    for (model, task, rep), pair in sorted(pairs.items()):
        if set(pair) != {a, b}:
            continue
        first, second = pair[a], pair[b]
        valid = first["status"] != "invalid" and second["status"] != "invalid"
        result["pairs"].append({"model": model, "task": task, "repetition": rep,
                                "statuses": {a: first["status"], b: second["status"]},
                                "valid_pair": valid,
                                "delta": {k: round(second[k] - first[k], 6)
                                          for k in ("elapsed_seconds", "agent_request_shapes", "tool_calls_main")}
                                if valid else None})
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("list")
    for name in ("plan", "run"):
        cmd = sub.add_parser(name)
        cmd.add_argument("--manifest", type=Path, required=True)
        if name == "run":
            cmd.add_argument("--output", type=Path, required=True)
            cmd.add_argument("--auth-source", type=Path, required=True)
            cmd.add_argument("--resume", action="store_true")
    compare = sub.add_parser("compare")
    compare.add_argument("output", type=Path)
    args = parser.parse_args()
    if args.command == "list":
        for case in CASES.values():
            print(case.id + "\t" + case.title + ("\tstep3" if case.step3 else "\tlong"))
        return 0
    if args.command == "compare":
        rows = [json.loads(p.read_text()) for p in sorted(args.output.glob("runs/*/result.json"))]
        print(json.dumps(comparison(rows), indent=2))
        return 0
    m = load_manifest(args.manifest)
    jobs = list(schedule(m))
    if args.command == "plan":
        print(json.dumps({"runs": len(jobs), "catalog_budget_guardrail_usd": len(jobs) * m["max_budget_usd"],
                          "note": "guardrail, not an estimate; in-flight requests can overshoot", "manifest": m}, indent=2))
        return 0
    args.output = args.output.resolve()
    if args.output.exists() and not args.resume:
        raise ValueError("output already exists; use --resume for an identical manifest")
    args.output.mkdir(parents=True, exist_ok=True, mode=0o700)
    saved = args.output / "manifest.json"
    if saved.exists() and json.loads(saved.read_text()) != m:
        raise ValueError("resume manifest/binary/instructions/suite mismatch")
    write_json(saved, m)
    rows = []
    with tempfile.TemporaryDirectory(prefix="moa-eval-batch-", dir="/tmp") as batch:
        for index, (rep, model, task, variant) in enumerate(jobs):
            output = args.output / "runs" / (str(index + 1).zfill(4) + "-" + variant["name"] + "-" + task)
            if args.resume and (output / "result.json").exists():
                rows.append(json.loads((output / "result.json").read_text()))
                continue
            output.mkdir(parents=True, exist_ok=True)
            row = run_one(m, variant, model, task, rep, output, args.auth_source, Path(batch) / "trial")
            rows.append(row)
            write_json(args.output / "summary.json", aggregate(rows))
            print(json.dumps({"run": index + 1, "total": len(jobs), "task": task, "variant": variant["name"],
                              "model": model, "status": row["status"], "elapsed": row["elapsed_seconds"]}), flush=True)
            if row["status"] == "invalid":
                print("Stopped on infrastructure/telemetry failure; inspect result before resuming", file=sys.stderr)
                return 2
    write_json(args.output / "summary.json", aggregate(rows))
    if any(r["status"] == "invalid" for r in rows):
        return 2
    return 1 if any(not r["passed"] for r in rows) else 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        print("eval error: " + str(error), file=sys.stderr)
        sys.exit(2)

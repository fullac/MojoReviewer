import io
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import types
import unittest
from unittest.mock import Mock, patch

sys.path.insert(0, str(Path(__file__).parent))
from start import Handler, Manager, atomic_json, valid_duration


class ManagerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.cfg = {"agent": {"api_key": "private-model-value", "model": "test-model", "max_context_tokens": 40000},
                    "github": {"webhook_secret": "private-signature-value", "token": "private-gh-value",
                               "trigger_user": "reviewer", "repos": ["acme/web"]},
                    "data_dir": str(self.root / "data"),
                    "manager": {"hooks": {"acme/web": 42}, "reviewer_account": "reviewer", "admin_account": "owner"}}
        atomic_json(self.root / ".MojoReviewer/config.json", self.cfg)
        self.m = Manager(self.root)

    def test_credentials_never_returned_and_blank_preserves(self):
        self.m.log("private-model-value private-signature-value private-gh-value")
        public = self.m.public_config()
        raw = json.dumps(public) + json.dumps(self.m.snapshot())
        for value in ("private-model-value", "private-signature-value", "private-gh-value"):
            self.assertNotIn(value, raw)
        self.assertTrue(public["agent"]["api_key_set"])
        self.m.save_config({"agent": {"model": "new", "api_key": ""}, "github": {"webhook_secret": ""}})
        disk = json.loads(self.m.path.read_text())
        self.assertEqual(disk["agent"]["api_key"], "private-model-value")
        self.assertEqual(disk["agent"]["max_context_tokens"], 40000)
        self.assertEqual(self.m.path.stat().st_mode & 0o777, 0o600)
        self.m.desired = True
        with self.assertRaises(ValueError):
            self.m.save_config({})

    def test_config_rejects_invalid_ranges_and_hook_scope(self):
        for cfg in ({"server": {"host": "0.0.0.0"}}, {"manager": {"hooks": {"other/repo": 9}}},
                    {"manager": {"hooks": {"acme/web": -1}}}, {"github": {"repos": ["../../private"]}}):
            with self.assertRaises(ValueError):
                self.m.save_config(cfg)

    def test_durations_match_go_positive_duration_format(self):
        for duration in ("300s","20m","1h2m3.5s",".5m","1ms"):
            self.assertTrue(valid_duration(duration))
        for duration in ("0s","-1s","1day","twenty","100000000000000h"):
            self.assertFalse(valid_duration(duration))
        with self.assertRaises(ValueError):
            self.m.save_config({"agent":{"task_timeout":"typo"}})

    def test_command_queue_bounded_and_manual_scope(self):
        self.m.request_action("start", {})
        with self.assertRaises(ValueError):
            self.m.request_action("stop", {})
        self.m.command = None
        with self.assertRaises(ValueError):
            self.m.request_action("retry", {"repo": "evil/repo", "number": 9})

    def test_health_failure_threshold_only_rebuilds_tunnel(self):
        self.m.desired = True
        self.m.phase = "running"
        child = Mock()
        child.poll.return_value = None
        self.m.children = {"reviewer": child, "tunnel": child}
        self.m.probe = Mock(return_value=False)
        self.m.rebuild = Mock()
        self.m.start_reviewer = Mock()
        self.m.stop_all = Mock()
        self.m.last_reconcile = time.monotonic()
        for attempt in range(3):
            self.m.last_probe = 0
            self.m.tick()
            self.assertEqual(self.m.rebuild.call_count, 1 if attempt == 2 else 0)
        self.m.start_reviewer.assert_not_called()
        self.m.stop_all.assert_not_called()

    def test_crash_backoff_and_stop_prevents_restart(self):
        self.m.desired = True
        child = Mock()
        child.poll.return_value = 1
        self.m.children = {"reviewer": child}
        self.m.stop_child = Mock(side_effect=lambda name: self.m.children.pop(name, None))
        self.m.start_reviewer = Mock()
        self.m.tick()
        self.assertGreater(self.m.next_start, time.monotonic())
        self.m.start_reviewer.assert_not_called()
        self.m.execute("stop", {})
        self.m.tick()
        self.assertFalse(self.m.desired)
        self.m.start_reviewer.assert_not_called()

    def test_hook_update_keeps_secret_and_only_adopted_id(self):
        self.m.url = "https://unit-test.trycloudflare.com"
        self.m.gh = Mock(return_value={"name": "web"})
        self.m.configure_hooks()
        calls = self.m.gh.call_args_list
        self.assertEqual(calls[0].args[0], "repos/acme/web/hooks/42")
        self.assertEqual(calls[1].args[1], "PATCH")
        self.assertEqual(calls[1].args[2]["config"]["secret"], "private-signature-value")
        self.assertEqual(calls[1].args[2]["config"]["insecure_ssl"], "0")

    def test_new_hook_id_persisted_before_next_repo(self):
        self.m.cfg["manager"]["hooks"]["acme/web"] = 0
        self.m.url = "https://unit-test.trycloudflare.com"
        self.m.gh = Mock(return_value={"id": 987})
        self.m.configure_hooks()
        self.assertEqual(json.loads(self.m.path.read_text())["manager"]["hooks"]["acme/web"], 987)

    def test_replay_filters_precision_dedup_and_unseen_heads(self):
        pr = {"number": 7, "state": "open", "head": {"sha": "new"},
              "base": {"repo": {"clone_url": "https://example.test/acme/web.git"}}}
        huge = 123456789012345678
        deliveries = [{"guid": "success", "event": "pull_request", "id": 1, "status_code": 200},
                      {"guid": "success", "event": "pull_request", "id": 2, "status_code": 503},
                      {"guid": "retry", "event": "pull_request", "id": huge, "status_code": 503},
                      {"guid": "stale", "event": "pull_request", "id": 4, "status_code": 503}]
        self.m.pages = Mock(side_effect=lambda url: deliveries if "deliveries" in url else [pr])
        def gh(url, method="GET", body=None):
            if url.endswith("/attempts"):
                return None
            return {"request": {"payload": {"action": "synchronize", "pull_request": {
                "number": 7, "head": {"sha": "old" if url.endswith("/4") else "new"}}}}}
        self.m.gh = Mock(side_effect=gh)
        self.m.signed = Mock(return_value=(202, '{"status":"queued"}'))
        self.m.reconcile()
        requests = [c.args[0] for c in self.m.gh.call_args_list if len(c.args) > 1 and c.args[1] == "POST"]
        self.assertEqual(requests, [f"repos/acme/web/hooks/42/deliveries/{huge}/attempts"])
        self.assertEqual(self.m.signed.call_args.args[1], "pull_request")
        self.m.reconcile()
        requests = [c for c in self.m.gh.call_args_list if len(c.args) > 1 and c.args[1] == "POST"]
        self.assertEqual(len(requests), 1)
        restarted = Manager(self.root)
        self.assertEqual(restarted.state["replays"]["acme/web:retry"]["count"], 1)

    def test_comment_replay_requires_trigger_and_skips_bot(self):
        current = {7: {}}
        payload = {"action": "created", "issue": {"number": 7, "pull_request": {}},
                   "comment": {"body": "@reviewer please"}}
        # GitHub pull_request marker contains a URL, not an empty object.
        payload["issue"]["pull_request"] = {"url": "https://example.test"}
        self.assertTrue(self.m.eligible("issue_comment", payload, current))
        for prefix in ("【MOJORviewer🥕】", "【MojoReviewer】"):
            payload["comment"]["body"] = prefix + " @reviewer"
            self.assertFalse(self.m.eligible("issue_comment", payload, current))
        payload["comment"]["body"] = "no trigger"
        self.assertFalse(self.m.eligible("issue_comment", payload, current))
        for comment in ("@reviewer-bot review", "@reviewer2", "@reviewer_team", "mail@reviewer.example", "@@reviewer", "@reviewer/team"):
            payload["comment"]["body"] = comment
            self.assertFalse(self.m.eligible("issue_comment", payload, current), comment)
        for comment in ("@REVIEWER: review", "请@reviewer，看看", "(@reviewer)."):
            payload["comment"]["body"] = comment
            self.assertTrue(self.m.eligible("issue_comment", payload, current), comment)

    def test_ready_for_review_replay_and_attempted_head_history(self):
        pr = {"number": 7, "state": "open", "head": {"sha": "A"},
              "base": {"repo": {"clone_url": "https://example.test/acme/web.git"}}}
        payload = {"action": "ready_for_review", "pull_request": pr}
        self.assertTrue(self.m.eligible("pull_request", payload, {7: pr}))
        pr["draft"] = True
        self.assertFalse(self.m.eligible("pull_request", payload, {7: pr}))
        pr["draft"] = False
        atomic_json(self.root / "data/reviews.json", {"acme/web#7": {"head_sha": "B", "auto_heads": ["A", "B"]}})
        self.m.pages = Mock(side_effect=lambda url: [] if "deliveries" in url else [pr])
        self.m.signed = Mock()
        self.m.reconcile()
        self.m.signed.assert_not_called()

    def test_manual_retry_is_local_and_does_not_post_github_comment(self):
        self.m.phase = "running"
        self.m.signed = Mock(return_value=(202, "queued"))
        self.m.gh = Mock()
        self.m.execute("retry", {"repo": "acme/web", "number": 7})
        self.assertEqual(self.m.signed.call_args.args[1], "issue_comment")
        self.m.gh.assert_not_called()

    def test_manual_retry_normalizes_trigger_and_matches_replay_parser(self):
        self.m.phase = "running"
        self.m.cfg["github"]["trigger_user"] = " @reviewer "
        self.m.signed = Mock(return_value=(202, "queued"))
        self.m.execute("retry", {"repo": "acme/web", "number": 7})
        payload = self.m.signed.call_args.args[2]
        self.assertTrue(payload["comment"]["body"].startswith("@reviewer "))
        self.assertNotIn("@@", payload["comment"]["body"])
        self.assertTrue(self.m.eligible("issue_comment", payload, {7: {}}))

    def test_replay_recovers_accepted_unstarted_comment_with_existing_head(self):
        pr = {"number": 7, "state": "open", "draft": False, "head": {"sha": "A"},
              "base": {"repo": {"clone_url": "https://example.test/acme/web.git"}}}
        records = {"acme/web#7": {"head_sha": "A", "auto_heads": ["A"]}}
        activity = [{"id": "github:comment-queued", "state": "interrupted", "stage": "queued",
                     "repo": "acme/web", "number": 7}]
        deliveries = [{"guid": "comment-queued", "event": "issue_comment", "id": 123, "status_code": 202},
                      {"guid": "comment-queued", "event": "issue_comment", "id": 124, "status_code": 503}]
        self.m.record_data = Mock(return_value=(records, activity))
        self.m.pages = Mock(side_effect=lambda url: deliveries if "deliveries" in url else [pr])
        payload = {"action": "created", "issue": {"number": 7, "pull_request": {"url": "x"}},
                   "comment": {"body": "@reviewer review focus on caller"}}
        self.m.gh = Mock(side_effect=lambda url, method="GET", body=None:
                         None if method == "POST" else {"request": {"payload": payload}})
        self.m.signed = Mock()
        self.m.reconcile()
        posts = lambda: [c.args[0] for c in self.m.gh.call_args_list if len(c.args) > 1 and c.args[1] == "POST"]
        self.assertEqual(posts(), ["repos/acme/web/hooks/42/deliveries/123/attempts"])
        self.m.signed.assert_not_called()
        # The same recovery still observes the existing interval and retry cap.
        self.m.reconcile()
        self.assertEqual(len(posts()), 1)
        for _ in range(2):
            self.m.state["replays"]["acme/web:comment-queued"]["time"] = time.time() - 121
            self.m.reconcile()
        self.m.state["replays"]["acme/web:comment-queued"]["time"] = time.time() - 121
        self.m.reconcile()
        self.assertEqual(len(posts()), 3)
        # Already-started or terminal attempts must not become automatic retries.
        for state, stage in (("interrupted", "shutdown"), ("failed", "model"), ("completed", "published")):
            activity[0].update(state=state, stage=stage)
            self.m.state["replays"] = {}
            self.m.gh.reset_mock()
            self.m.reconcile()
            self.m.gh.assert_not_called()

    def test_pages_preserve_cursor_and_large_integer(self):
        self.m.token = Mock(return_value="private-token")
        with patch("start.subprocess.run", return_value=types.SimpleNamespace(returncode=0, stdout='[[{"id":123456789012345678}],[{"id":2}]]')) as run:
            rows = self.m.pages("repos/acme/web/hooks/42/deliveries")
            self.assertEqual(rows[0]["id"], 123456789012345678)
            self.assertIn("--paginate", run.call_args.args[0])
            self.assertEqual(run.call_args.kwargs["env"]["GH_TOKEN"], "private-token")

    def test_handler_rejects_cross_origin_dns_rebinding_and_missing_csrf(self):
        handler = object.__new__(Handler)
        handler.server = types.SimpleNamespace(server_port=9970, manager=self.m)
        handler.respond = Mock()
        handler.headers = {"Host": "evil.test:9970"}
        self.assertFalse(handler.authorized())
        handler.headers = {"Host": "127.0.0.1:9970", "Origin": "https://evil.test"}
        self.assertFalse(handler.authorized())
        handler.headers = {"Host": "127.0.0.1:9970", "Content-Type": "application/json"}
        self.assertFalse(handler.authorized(True))
        handler.headers["X-CSRF-Token"] = self.m.csrf
        self.assertTrue(handler.authorized(True))

    def test_stop_kills_owned_process_group(self):
        process = self.m.spawn("reviewer", [sys.executable, "-u", "-c", "import time;print('ready');time.sleep(60)"])
        try:
            self.m.stop_child("reviewer")
            self.assertIsNotNone(process.poll())
            self.assertNotIn("reviewer", self.m.children)
        finally:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()

    def test_uncertain_creation_not_retried_automatically(self):
        self.m.cfg["manager"]["hooks"]["acme/web"] = 0
        self.m.url = "https://unit-test.trycloudflare.com"
        self.m.gh = Mock(side_effect=RuntimeError("uncertain result"))
        with self.assertRaises(RuntimeError):
            self.m.configure_hooks()
        with self.assertRaises(ValueError):
            self.m.configure_hooks()
        self.assertEqual(self.m.gh.call_count, 1)

    def test_progress_reads_only_sanitized_attempt_file(self):
        atomic_json(self.root / "data/progress/github:abc.json", [
            {"at": "2026-10-25T00:00:00Z", "kind": "tool", "summary": "read_repo_file a.go 1-20"},
            {"kind": "note", "summary": "严重 a.go:12 预算判断失效 " + "x" * 400},
            {"summary": "missing kind"},
        ])
        got = self.m.progress("github:abc")
        self.assertEqual(got["events"][0]["summary"], "read_repo_file a.go 1-20")
        self.assertEqual(len(got["events"]), 2)
        self.assertLessEqual(len(got["events"][1]["summary"]), 240)
        raw = json.dumps(got)
        self.assertNotIn("/home/", raw)
        with self.assertRaises(ValueError):
            self.m.progress("../activity")
        self.assertEqual(self.m.progress("missing")["events"], [])

    def test_legacy_records_are_not_success_receipts(self):
        atomic_json(self.root / "data/reviews.json", {"acme/web#7": {"session_id": "legacy"}})
        self.assertEqual(self.m.snapshot()["activity"], [])
        atomic_json(self.root / "data/activity.json", [{"state": "running"}])
        self.assertEqual(self.m.snapshot()["activity"][0]["state"], "interrupted")


if __name__ == "__main__":
    unittest.main()

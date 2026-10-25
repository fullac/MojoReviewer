#!/usr/bin/env python3
"""Local MojoReviewer supervisor and GUI. Python 3.10+, gh, cloudflared required."""
import argparse
import collections
import hashlib
import hmac
import http.server
import json
import os
from pathlib import Path
import re
import secrets
import signal
import socket
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
REPO = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\Z")
SECRET = re.compile(r"(?:gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+|sk-[A-Za-z0-9_-]+)")


def atomic_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + "." + secrets.token_hex(8))
    try:
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as f:
            json.dump(value, f, ensure_ascii=False, indent=2)
            f.write("\n")
        os.replace(tmp, path)
    finally:
        tmp.unlink(missing_ok=True)


def valid_duration(value):
    if not isinstance(value,str):
        return False
    parts = re.findall(r"(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)", value)
    if not parts or "".join(number+unit for number,unit in parts) != value:
        return False
    scale={"ns":1e-9,"us":1e-6,"µs":1e-6,"μs":1e-6,"ms":1e-3,"s":1,"m":60,"h":3600}
    total=sum(float(number)*scale[unit] for number,unit in parts)
    return 0 < total < 9223372036


def read_json(path, default):
    try:
        return json.loads(path.read_text())
    except FileNotFoundError:
        return default


class Manager:
    def __init__(self, root=ROOT, binary=None):
        self.root = Path(root).resolve()
        self.binary = str(binary or self.root / "bin/mojoreviewer")
        self.path = self.root / ".MojoReviewer/config.json"
        self.cfg = read_json(self.path, {})
        self.state_path = self.root / ".MojoReviewer/manager-state.json"
        self.state = read_json(self.state_path, {"replays": {}})
        self.lock = threading.RLock()
        self.logs = collections.deque(maxlen=400)
        self.children = {}
        self.desired = False
        self.url = ""
        self.phase = "stopped"
        self.error = ""
        self.failures = 0
        self.next_start = 0
        self.backoff = 2
        self.command = None
        self.wake = threading.Event()
        self.exit = threading.Event()
        self.csrf = secrets.token_urlsafe(32)
        self.tokens = []
        self.last_reconcile = 0
        self.last_probe = 0

    def redact(self, text):
        for token in self.tokens + [self.cfg.get("agent", {}).get("api_key", ""),
                                  self.cfg.get("github", {}).get("token", ""),
                                  self.cfg.get("github", {}).get("webhook_secret", "")]:
            if token:
                text = text.replace(token, "[redacted]")
        return SECRET.sub("[redacted]", text)

    def log(self, message):
        with self.lock:
            self.logs.append(time.strftime("%H:%M:%S ") + self.redact(str(message))[:8192])

    def local_url(self):
        server = self.cfg.get("server", {})
        if server.get("host", "127.0.0.1") not in ("127.0.0.1", "localhost"):
            raise ValueError("管理模式要求 reviewer 监听 127.0.0.1")
        return "http://127.0.0.1:" + str(server.get("port", 9968))

    def token(self, account):
        host = self.cfg.get("github", {}).get("host") or "github.com"
        result = subprocess.run(["gh", "auth", "token", "--hostname", host, "--user", account],
                                capture_output=True, text=True, timeout=10)
        if result.returncode or not result.stdout.strip():
            raise ValueError("gh 中未登录所选账户，请先 gh auth login")
        token = result.stdout.strip()
        with self.lock:
            if token not in self.tokens:
                self.tokens.append(token)
        return token

    def gh(self, endpoint, method="GET", body=None, admin=True):
        settings = self.cfg.get("manager", {})
        account = settings.get("admin_account", "fullac") if admin else settings.get("reviewer_account", "raymojo42")
        env = dict(os.environ, GH_TOKEN=self.token(account))
        host = self.cfg.get("github", {}).get("host") or "github.com"
        args = ["gh", "api", "--hostname", host, endpoint, "--method", method]
        if body is not None:
            args += ["--input", "-"]
        result = subprocess.run(args, input=json.dumps(body) if body is not None else None,
                                capture_output=True, text=True, env=env, timeout=25)
        if result.returncode:
            # Raw stderr can contain credentials and internal endpoints. Keep it out of the API/log.
            raise RuntimeError("GitHub API 失败，请检查账户、仓库权限和网络")
        return json.loads(result.stdout) if result.stdout.strip() else None

    def pages(self, endpoint):
        # Cursor pagination for deliveries, numbered pagination for other endpoints.
        # Parse gh's Link headers would add a second parser; --paginate --slurp handles both.
        settings = self.cfg.get("manager", {})
        env = dict(os.environ, GH_TOKEN=self.token(settings.get("admin_account", "fullac")))
        host = self.cfg.get("github", {}).get("host") or "github.com"
        result = subprocess.run(["gh", "api", "--hostname", host, endpoint + "?per_page=100",
                                 "--paginate", "--slurp"], capture_output=True, text=True, env=env, timeout=45)
        if result.returncode:
            raise RuntimeError("无法读取 GitHub 列表，请检查权限和网络")
        return [row for page in json.loads(result.stdout) for row in page]

    def signed(self, base, event, body):
        raw = json.dumps(body, separators=(",", ":")).encode()
        secret = self.cfg.get("github", {}).get("webhook_secret", "")
        signature = hmac.new(secret.encode(), raw, hashlib.sha256).hexdigest()
        req = urllib.request.Request(base + "/webhook/github", raw,
                {"Content-Type": "application/json", "X-GitHub-Event": event,
                 "X-Hub-Signature-256": "sha256=" + signature})
        # Never forward the signature through redirects to an unrelated endpoint.
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, *args):
                return None
        handlers = [NoRedirect()]
        if base.startswith("http://127.0.0.1:"):
            handlers.append(urllib.request.ProxyHandler({}))
        with urllib.request.build_opener(*handlers).open(req, timeout=10) as response:
            return response.status, response.read().decode()

    def probe(self, base):
        try:
            code, body = self.signed(base, "ping", {})
            return code == 200 and json.loads(body).get("status") == "ignored"
        except (OSError, ValueError, urllib.error.URLError):
            return False

    def spawn(self, name, args, env=None):
        process = subprocess.Popen(args, cwd=self.root, env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, text=True, start_new_session=True)
        with self.lock:
            self.children[name] = process
        threading.Thread(target=self.output, args=(name, process), daemon=True).start()
        return process

    def output(self, name, process):
        try:
            for line in process.stdout:
                if name == "tunnel":
                    match = re.search(r"https://[a-z0-9-]+\.trycloudflare\.com", line)
                    if match:
                        with self.lock:
                            if self.children.get(name) is process:
                                self.url = match.group()
                self.log(name + ": " + line.rstrip())
        finally:
            process.stdout.close()

    def stop_child(self, name):
        with self.lock:
            process = self.children.pop(name, None)
            if name == "tunnel":
                self.url = ""
        if process:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=8)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait(timeout=3)

    def stop_all(self):
        self.stop_child("tunnel")
        self.stop_child("reviewer")
        with self.lock:
            self.phase = "stopped"
            self.failures = 0

    def start_reviewer(self):
        self.cfg = read_json(self.path, {})
        if not self.cfg.get("agent", {}).get("api_key") or not self.cfg.get("github", {}).get("webhook_secret"):
            raise ValueError("请先填写模型 API Key 和 webhook secret")
        if not Path(self.binary).is_file():
            raise ValueError("未找到 reviewer 二进制；先 go build -o bin/mojoreviewer ./cmd/mojoreviewer")
        port = int(self.cfg.get("server", {}).get("port", 9968))
        self.local_url()
        with socket.socket() as sock:
            if sock.connect_ex(("127.0.0.1", port)) == 0:
                raise ValueError("reviewer 端口已被占用；请先关闭原启动器，本管理器不会接管其他进程")
        settings = self.cfg.get("manager", {})
        env = dict(os.environ, GH_TOKEN=self.token(settings.get("reviewer_account", "raymojo42")))
        self.spawn("reviewer", [self.binary], env)
        self.phase = "starting"
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline and not self.exit.is_set():
            if self.command is not None:
                return
            if self.probe(self.local_url()):
                return
            if self.children["reviewer"].poll() is not None:
                break
            self.exit.wait(0.5)
        raise RuntimeError("reviewer 启动失败，查看日志")

    def rebuild(self):
        self.phase = "recovering"
        self.stop_child("tunnel")
        self.spawn("tunnel", ["cloudflared", "tunnel", "--no-autoupdate", "--url", self.local_url()])
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline and not self.exit.is_set():
            if self.command is not None:
                return
            if self.url and self.probe(self.url):
                self.configure_hooks()
                self.failures = 0
                self.phase = "running"
                self.error = ""
                self.last_probe = time.monotonic()
                self.log("隧道已恢复，webhook 已更新")
                self.reconcile()
                return
            if self.children["tunnel"].poll() is not None:
                break
            self.exit.wait(1)
        raise RuntimeError("隧道未通过签名探测，稍后自动重试")

    def configure_hooks(self):
        hooks = self.cfg.get("manager", {}).get("hooks", {})
        if not hooks:
            raise ValueError("请配置仓库和 webhook ID（新建填 0）")
        for repo, hook in hooks.items():
            if self.exit.is_set() or self.command is not None:
                return
            if repo not in self.cfg.get("github", {}).get("repos", []):
                raise ValueError("webhook 仓库必须在 github.repos 白名单内")
            payload = {"active": True, "events": ["pull_request", "issue_comment"],
                       "config": {"url": self.url + "/webhook/github", "content_type": "json",
                                  "secret": self.cfg["github"]["webhook_secret"], "insecure_ssl": "0"}}
            if hook:
                # IDs are explicitly adopted in config. No guessing or replacing unrelated hooks.
                current = self.gh(f"repos/{repo}/hooks/{int(hook)}")
                if current.get("name") != "web":
                    raise ValueError("所选 webhook 不是 web 类型")
                self.gh(f"repos/{repo}/hooks/{int(hook)}", "PATCH", payload)
            else:
                if self.state.get("creating") == repo:
                    raise ValueError("上次创建 webhook 未确认；请在 GitHub 查看已有 ID 后填入，避免重复创建")
                self.state["creating"] = repo
                atomic_json(self.state_path, self.state)
                created = self.gh(f"repos/{repo}/hooks", "POST", dict(payload, name="web"))
                hooks[repo] = created["id"]
                atomic_json(self.path, self.cfg)
                self.state.pop("creating", None)
                atomic_json(self.state_path, self.state)

    def record_data(self):
        directory = Path(self.cfg.get("data_dir") or Path.home() / ".mojoreviewer").expanduser()
        records = read_json(directory / "reviews.json", {})
        activities = read_json(directory / "activity.json", [])
        for item in activities:
            if item.get("state") in ("queued", "running") and not self.children.get("reviewer"):
                if item["state"] == "queued":
                    item["stage"] = "queued"
                item["state"] = "interrupted"
        return records, activities

    def reconcile(self):
        """Replay failed/unstarted deliveries and recover unseen open PR heads."""
        self.last_reconcile = time.monotonic()
        records, activities = self.record_data()
        replays = self.state.setdefault("replays", {})
        for repo, hook in self.cfg.get("manager", {}).get("hooks", {}).items():
            if self.exit.is_set() or self.command is not None:
                return
            prs = self.pages(f"repos/{repo}/pulls")
            current = {pr["number"]: pr for pr in prs if pr.get("state") == "open" and not pr.get("draft")}
            deliveries = self.pages(f"repos/{repo}/hooks/{int(hook)}/deliveries")
            unstarted = {a["id"].removeprefix("github:") for a in activities
                         if a.get("repo") == repo and a.get("state") == "interrupted"
                         and a.get("stage") == "queued" and a.get("id", "").startswith("github:")}
            succeeded = {d["guid"] for d in deliveries if 200 <= (d.get("status_code") or 0) < 300}
            seen = set()
            for delivery in deliveries:
                if self.exit.is_set() or self.command is not None:
                    return
                guid = delivery["guid"]
                if (guid in succeeded and guid not in unstarted) or guid in seen or delivery.get("event") not in ("pull_request", "issue_comment"):
                    continue
                seen.add(guid)
                key = repo + ":" + guid
                previous = replays.get(key, {})
                if previous.get("count", 0) >= 3 or time.time() - previous.get("time", 0) < 120:
                    continue
                detail = self.gh(f"repos/{repo}/hooks/{int(hook)}/deliveries/{int(delivery['id'])}")
                payload = detail.get("request", {}).get("payload", {})
                if isinstance(payload, str):
                    payload = json.loads(payload)
                if not self.eligible(delivery["event"], payload, current):
                    continue
                replays[key] = {"count": previous.get("count", 0) + 1, "time": time.time()}
                # Bounded metadata only; IDs stay Python integers, never rounded by JavaScript.
                if len(replays) > 512:
                    del replays[min(replays, key=lambda k: replays[k]["time"])]
                atomic_json(self.state_path, self.state)
                self.gh(f"repos/{repo}/hooks/{int(hook)}/deliveries/{int(delivery['id'])}/attempts", "POST")
                self.log(f"已请求补送 {repo} 的失败或未启动 {delivery['event']} 事件")
            for number, pr in current.items():
                if self.exit.is_set() or self.command is not None:
                    return
                record = records.get(f"{repo}#{number}", {})
                auto_heads = record.get("auto_heads")
                if pr["head"]["sha"] in (auto_heads if auto_heads is not None else [record.get("head_sha")]):
                    continue
                # Signed local delivery respects the reviewer's queue/deduplication/allow-list.
                payload = {"action": "synchronize", "pull_request": pr,
                           "repository": {"full_name": repo, "clone_url": pr["base"]["repo"]["clone_url"]}}
                try:
                    self.signed(self.local_url(), "pull_request", payload)
                except urllib.error.HTTPError as error:
                    if error.code == 429:
                        self.log("审查队列已满，下轮继续补齐")
                        return
                    raise
        self.log("已检查失败投递和未审查的 PR head")

    def eligible(self, event, payload, current):
        if event == "pull_request":
            pr = payload.get("pull_request", {})
            now = current.get(pr.get("number"))
            return bool(now and payload.get("action") in ("opened", "reopened", "synchronize", "ready_for_review")
                        and not pr.get("draft") and pr.get("head", {}).get("sha") == now["head"]["sha"])
        comment = payload.get("comment", {}).get("body", "")
        trigger = self.cfg.get("github", {}).get("trigger_user", "").strip().removeprefix("@")
        return bool(payload.get("action") == "created" and payload.get("issue", {}).get("number") in current
                    and payload.get("issue", {}).get("pull_request") and trigger
                    and re.search(r"(?<![A-Za-z0-9_.+@-])@" + re.escape(trigger) + r"(?![A-Za-z0-9_/@-])",
                                  comment, re.IGNORECASE | re.ASCII)
                    and not comment.lstrip().startswith(("【MOJORviewer🥕】", "【MojoReviewer】")))

    def request_action(self, action, body):
        with self.lock:
            if self.command is not None:
                raise ValueError("已有管理操作等待执行")
            if action not in ("start", "stop", "restart", "tunnel", "retry", "reconcile"):
                raise ValueError("未知操作")
            if action == "retry":
                repo, number = body.get("repo", ""), body.get("number", 0)
                if repo not in self.cfg.get("github", {}).get("repos", []) or type(number) is not int or number < 1:
                    raise ValueError("请选择允许的仓库和有效 PR 编号")
            self.command = (action, body)
            self.wake.set()

    def execute(self, action, body):
        if action == "stop":
            self.desired = False
            self.error = ""
            self.stop_all()
        elif action in ("start", "restart"):
            if action == "restart":
                self.stop_all()
            self.desired = True
            self.next_start = 0
        elif action == "tunnel":
            if not self.desired or not self.probe(self.local_url()):
                raise ValueError("请先启动 reviewer")
            self.rebuild()
        elif action == "reconcile":
            if self.phase != "running":
                raise ValueError("入口尚未就绪")
            self.reconcile()
        elif action == "retry":
            if self.phase != "running":
                raise ValueError("请先启动服务")
            trigger = self.cfg.get("github", {}).get("trigger_user", "").strip().removeprefix("@")
            if not trigger:
                raise ValueError("请先配置 trigger_user")
            status, _ = self.signed(self.local_url(), "issue_comment", {
                "action": "created", "repository": {"full_name": body["repo"]},
                "issue": {"number": body["number"], "pull_request": {"url": "manager"}},
                "comment": {"body": "@" + trigger + " 请重新审查当前 PR", "user": {"login": "local-manager"}}})
            if status != 202:
                raise ValueError("审查未入队")
            self.log(f"重新审查已入队：{body['repo']}#{body['number']}")

    def tick(self):
        now = time.monotonic()
        if self.command is not None:
            return
        reviewer = self.children.get("reviewer")
        if reviewer and reviewer.poll() is not None:
            self.stop_all()
            self.next_start = now + self.backoff
            self.backoff = min(60, self.backoff * 2)
            self.error = "reviewer 退出，将自动重启"
        if not self.desired or now < self.next_start:
            return
        if not self.children.get("reviewer"):
            self.start_reviewer()
        if self.command is not None or self.exit.is_set():
            return
        tunnel = self.children.get("tunnel")
        if not tunnel or tunnel.poll() is not None or self.phase != "running":
            self.rebuild()
        if self.command is not None or self.exit.is_set():
            return
        if now - self.last_probe >= 30:
            self.last_probe = now
            if self.probe(self.url):
                self.failures = 0
                self.backoff = 2
                self.error = ""
            else:
                self.failures += 1
                self.log(f"入口签名探测失败 {self.failures}/3")
                if self.failures >= 3:
                    self.rebuild()
        if now - self.last_reconcile >= 60 and self.failures == 0:
            self.reconcile()

    def run(self):
        while not self.exit.is_set():
            try:
                with self.lock:
                    command, self.command = self.command, None
                if command:
                    self.execute(*command)
                self.tick()
            except Exception as error:
                self.error = self.redact(str(error))
                self.log(self.error)
                if self.phase != "running":
                    self.phase = "error"
                    self.next_start = time.monotonic() + self.backoff
                    self.backoff = min(60, self.backoff * 2)
                    self.stop_child("tunnel")
            self.wake.wait(2)
            self.wake.clear()
        self.stop_all()

    def progress(self, activity_id):
        if not re.fullmatch(r"[A-Za-z0-9:_-]{1,80}", activity_id or "") or ".." in activity_id:
            raise ValueError("尝试 ID 无效")
        directory = Path(self.cfg.get("data_dir") or Path.home() / ".mojoreviewer").expanduser()
        rows = read_json(directory / "progress" / f"{activity_id}.json", [])
        if not isinstance(rows, list):
            rows = []
        events = []
        for row in rows[-400:]:
            if not isinstance(row, dict):
                continue
            kind, summary = str(row.get("kind") or "")[:40], str(row.get("summary") or "")[:240]
            if not kind or not summary:
                continue
            events.append({"at": str(row.get("at") or "")[:40], "kind": kind, "summary": summary})
        return {"id": activity_id, "events": events}

    def snapshot(self):
        with self.lock:
            _, activity = self.record_data()
            return {"phase": self.phase, "desired": self.desired, "url": self.url,
                    "error": self.error, "pending": self.command is not None,
                    "pids": {name: p.pid for name, p in self.children.items() if p.poll() is None},
                    "health_failures": self.failures, "activity": activity[::-1],
                    "logs": list(self.logs), "csrf": self.csrf}

    def public_config(self):
        with self.lock:
            cfg = json.loads(json.dumps(self.cfg))
        for section, keys in {"agent": ["api_key"], "github": ["token", "webhook_secret"]}.items():
            for key in keys:
                values = cfg.setdefault(section, {})
                values[key + "_set"] = bool(values.pop(key, ""))
        cfg.setdefault("manager", {"reviewer_account": "raymojo42", "admin_account": "fullac", "hooks": {}})
        return cfg

    def save_config(self, patch):
        with self.lock:
            if self.desired or self.children:
                raise ValueError("请先停止 reviewer 再保存配置")
            # Merge only documented keys; preserve SDK and user-added settings on disk.
            cfg = json.loads(json.dumps(self.cfg))
            allowed = {"agent": {"provider", "model", "api_base", "api_key", "task_timeout", "request_timeout", "max_context_tokens"},
                       "github": {"host", "repos", "trigger_user", "webhook_secret"},
                       "server": {"host", "port"},
                       "manager": {"reviewer_account", "admin_account", "hooks", "auto_start"}}
            for section, keys in allowed.items():
                value = patch.get(section, {})
                if not isinstance(value, dict):
                    raise ValueError("配置格式无效")
                for key in keys & value.keys():
                    if key in ("api_key", "webhook_secret") and not value[key]:
                        continue  # Blank secrets mean retain, never expose existing values.
                    cfg.setdefault(section, {})[key] = value[key]
            gh, server = cfg.get("github", {}), cfg.get("server", {})
            if not isinstance(gh.get("repos", []), list) or any(not isinstance(r, str) or not REPO.fullmatch(r) for r in gh.get("repos", [])):
                raise ValueError("仓库格式为 owner/repo")
            if server.get("host", "127.0.0.1") not in ("127.0.0.1", "localhost") or type(server.get("port", 9968)) is not int or not 1 <= server.get("port", 9968) <= 65535:
                raise ValueError("监听地址或端口无效")
            settings = cfg.get("manager", {})
            if not isinstance(settings.get("hooks", {}), dict) or any(r not in gh.get("repos", []) or type(h) is not int or h < 0 for r, h in settings.get("hooks", {}).items()):
                raise ValueError("hook ID 必须是非负整数，仓库必须在白名单内")
            for key in ("admin_account", "reviewer_account"):
                if not re.fullmatch(r"[A-Za-z0-9-]{1,39}", settings.get(key, "fullac")):
                    raise ValueError("GitHub 账户名无效")
            for key in ("provider", "model", "api_base", "api_key", "task_timeout", "request_timeout"):
                if not isinstance(cfg.get("agent", {}).get(key, ""), str):
                    raise ValueError("模型配置必须是文本")
            if type(cfg.get("agent", {}).get("max_context_tokens",40000)) is not int or cfg.get("agent", {}).get("max_context_tokens",40000)<1000:
                raise ValueError("上下文上限至少为 1000")
            if not isinstance(gh.get("webhook_secret", ""), str) or not isinstance(gh.get("trigger_user", ""), str):
                raise ValueError("GitHub 配置必须是文本")
            for key, default in (("task_timeout","20m"),("request_timeout","5m")):
                if not valid_duration(cfg.get("agent",{}).get(key) or default):
                    raise ValueError("超时格式为正时长，例如 300s 或 20m")
            host=gh.get("host","")
            if not isinstance(host,str) or any(ch in host for ch in " \t/\\:@"):
                raise ValueError("GitHub 主机只填主机名")
            if type(settings.get("auto_start",False)) is not bool:
                raise ValueError("auto_start 必须是布尔值")
            atomic_json(self.path, cfg)
            self.cfg = cfg
            self.error = ""


class Handler(http.server.BaseHTTPRequestHandler):
    def setup(self):
        super().setup()
        self.connection.settimeout(10)

    def log_message(self, *args):
        pass

    def respond(self, status, value, content="application/json; charset=utf-8"):
        raw = json.dumps(value, ensure_ascii=False).encode() if content.startswith("application/json") else value
        self.send_response(status)
        self.send_header("Content-Type", content)
        self.send_header("Content-Length", str(len(raw)))
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
        self.end_headers()
        self.wfile.write(raw)

    def authorized(self, mutation=False):
        expected = f"127.0.0.1:{self.server.server_port}"
        if self.headers.get("Host") != expected:
            self.respond(403, {"error": "仅允许本机管理入口"})
            return False
        origin = self.headers.get("Origin")
        if origin and origin != "http://" + expected:
            self.respond(403, {"error": "拒绝跨站请求"})
            return False
        if mutation and (self.headers.get("X-CSRF-Token") != self.server.manager.csrf or
                         self.headers.get("Content-Type") != "application/json"):
            self.respond(403, {"error": "管理请求验证失败"})
            return False
        return True

    def do_GET(self):
        if not self.authorized():
            return
        manager = self.server.manager
        try:
            if self.path == "/api/status":
                self.respond(200, manager.snapshot())
            elif self.path == "/api/config":
                self.respond(200, manager.public_config())
            elif self.path.startswith("/api/activity/"):
                activity_id = urllib.parse.unquote(self.path.removeprefix("/api/activity/"))
                if activity_id.endswith("/progress"):
                    activity_id = activity_id.removesuffix("/progress")
                self.respond(200, manager.progress(activity_id))
            elif self.path in ("/", "/app.js", "/style.css"):
                name, mime = {"/": ("index.html", "text/html; charset=utf-8"),
                              "/app.js": ("app.js", "text/javascript; charset=utf-8"),
                              "/style.css": ("style.css", "text/css; charset=utf-8")}[self.path]
                self.respond(200, (ROOT / "ops/web" / name).read_bytes(), mime)
            else:
                self.respond(404, {"error": "不存在"})
        except Exception:
            self.respond(500, {"error": "读取状态失败"})

    def do_POST(self):
        if not self.authorized(True):
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if not 0 < length <= 65536:
                raise ValueError("请求大小无效")
            body = json.loads(self.rfile.read(length))
            if not isinstance(body, dict):
                raise ValueError("请求格式无效")
            if self.path == "/api/config":
                self.server.manager.save_config(body)
                self.respond(200, {"status": "saved"})
            elif self.path.startswith("/api/action/"):
                self.server.manager.request_action(self.path.removeprefix("/api/action/"), body)
                self.respond(202, {"status": "accepted"})
            else:
                self.respond(404, {"error": "不存在"})
        except (ValueError, TypeError) as error:
            self.respond(400, {"error": self.server.manager.redact(str(error))})
        except Exception:
            self.respond(500, {"error": "操作失败，查看管理器日志"})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=ROOT)
    parser.add_argument("--binary", type=Path)
    parser.add_argument("--port", type=int, default=9970)
    parser.add_argument("--start", action="store_true", help="打开 GUI 时同时启动服务")
    args = parser.parse_args()
    manager = Manager(args.root, args.binary)
    # Process-wide file lock prevents duplicate managers controlling the same config.
    import fcntl
    lock_path = manager.root / ".MojoReviewer/manager.lock"
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    lock_file = open(lock_path, "a")
    try:
        fcntl.flock(lock_file, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        parser.error("该仓库已有管理器运行")
    server = http.server.ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    server.manager = manager
    manager.desired = args.start or bool(manager.cfg.get("manager", {}).get("auto_start", False))
    worker = threading.Thread(target=manager.run)
    worker.start()
    def shutdown(*_):
        manager.exit.set()
        manager.wake.set()
        threading.Thread(target=server.shutdown, daemon=True).start()
    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)
    print(f"MojoReviewer 管理器：http://127.0.0.1:{args.port}", flush=True)
    try:
        server.serve_forever()
    finally:
        manager.exit.set()
        manager.wake.set()
        worker.join()
        server.server_close()
        lock_file.close()


if __name__ == "__main__":
    main()

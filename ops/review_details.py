"""Read-only review inspector. Never accepts a filesystem path or remote URL."""
import base64
import hashlib
import json
import re
from datetime import datetime
from pathlib import Path
from urllib.parse import urlsplit

SESSION_ID = re.compile(r"[A-Za-z0-9_-]{1,128}\Z")
REPO = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\Z")
BLOB = re.compile(r"[a-f0-9]{64}\Z")
MAX_FILE = 64 * 1024 * 1024
PREVIEW = 12000
BLOCKS = ("content", "reasoning_content", "tool_calls", "content_parts", "attachments", "metadata")


class DetailError(Exception):
    def __init__(self, message, status=400):
        super().__init__(message)
        self.status = status


def read_inside(root, relative, limit=MAX_FILE):
    root = Path(root).resolve()
    if Path(relative).is_absolute() or ".." in Path(relative).parts:
        raise DetailError("记录路径超出允许范围", 403)
    path = root / relative
    try:
        resolved = path.resolve(strict=True)
        resolved.relative_to(root)
        if not resolved.is_file():
            raise DetailError("记录不是普通文件", 422)
        with resolved.open("rb") as stream:
            raw = stream.read(limit + 1)
    except FileNotFoundError:
        raise DetailError("本地记录不存在", 404) from None
    except (ValueError, RuntimeError):
        raise DetailError("记录路径超出允许范围", 403) from None
    except OSError:
        raise DetailError("本地记录暂时无法读取", 503) from None
    if len(raw) > limit:
        raise DetailError("记录超过读取上限，未截断或伪装成完整内容", 413)
    return raw


def decode_json(raw):
    try:
        value = json.loads(raw)
    except (ValueError, UnicodeError):
        raise DetailError("记录格式损坏，无法完整读取", 422) from None
    if not isinstance(value, dict):
        raise DetailError("记录格式不支持", 422)
    return value


def timestamp(value):
    try:
        return datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()
    except (TypeError, ValueError, AttributeError):
        return None


def scrub(manager, value):
    if isinstance(value, str):
        return manager.redact(value).replace(str(Path.home()), "~")
    if isinstance(value, list):
        return [scrub(manager, item) for item in value]
    if isinstance(value, dict):
        return {key: scrub(manager, item) for key, item in value.items()}
    return value


class ReviewDetails:
    def __init__(self, manager, attempt_id):
        if not isinstance(attempt_id, str) or not attempt_id or len(attempt_id) > 512:
            raise DetailError("缺少有效的审查记录 ID")
        self.manager = manager
        self.data = Path(manager.cfg.get("data_dir") or Path.home() / ".mojoreviewer").expanduser().resolve()
        _, rows = manager.record_data()
        self.activity = next((row for row in rows if row.get("id") == attempt_id), None)
        if not self.activity:
            raise DetailError("审查记录不存在或已超出保留范围", 404)
        repo = self.activity.get("repo", "")
        number = self.activity.get("number")
        if (not isinstance(repo, str) or not REPO.fullmatch(repo) or repo not in manager.cfg.get("github", {}).get("repos", [])
                or type(number) is not int or number < 1):
            raise DetailError("该审查不在受管仓库范围内", 403)
        self.repo, self.number = repo, number
        self.title = f"{repo}#{number}"
        self.sessions = self.data / "luckyagent/sessions"

    def content(self):
        """The final published comment is host-rendered, not assistant prose."""
        url = self.activity.get("comment_url", "")
        if not url:
            return {"available": False, "reason": "这条审查尚无已发布评论；会话回复不等于宿主拼装的最终正文。"}
        host = self.manager.cfg.get("github", {}).get("host") or "github.com"
        try:
            parsed = urlsplit(url)
            match = re.fullmatch(r"issuecomment-([0-9]+)", parsed.fragment)
            valid = (parsed.scheme == "https" and parsed.netloc.casefold() == host.casefold()
                     and not parsed.username and not parsed.password and not parsed.query
                     and parsed.path == f"/{self.repo}/pull/{self.number}" and match)
        except ValueError:
            valid = False
        if not valid:
            raise DetailError("评论链接与受管 PR 或 GitHub 主机不一致", 422)
        comment_id = int(match.group(1))
        try:
            comment = self.manager.gh(f"repos/{self.repo}/issues/comments/{comment_id}")
        except Exception:
            raise DetailError("无法读取 GitHub 评论；请检查权限、网络或评论是否已删除", 502) from None
        if (not isinstance(comment, dict) or comment.get("id") != comment_id or comment.get("html_url") != url
                or not isinstance(comment.get("body"), str)):
            raise DetailError("GitHub 返回的评论与当前记录不匹配", 502)
        return scrub(self.manager, {"available": True, "source": "github", "body": comment["body"],
                                   "url": url, "created_at": comment.get("created_at"),
                                   "updated_at": comment.get("updated_at"), "author": comment.get("user", {}).get("login", "")})

    def metadata(self, sid):
        if not isinstance(sid, str) or not SESSION_ID.fullmatch(sid):
            raise DetailError("会话 ID 无效", 403)
        meta = decode_json(read_inside(self.sessions, sid + ".meta.json", 1024 * 1024))
        if meta.get("id") != sid or meta.get("title") not in (self.title, self.title + ":fix"):
            raise DetailError("会话与当前 PR 不匹配", 403)
        return meta

    def candidates(self):
        exact = self.activity.get("session_id")
        if exact:
            return [self.metadata(exact)], "exact", exact
        # Old receipts have no immutable session binding. Only inspect sessions
        # whose own persisted title matches this PR, never use latest-PR state as proof.
        candidates = []
        end = timestamp(self.activity.get("updated_at"))
        if self.sessions.exists():
            for path in self.sessions.glob("*.meta.json"):
                sid = path.name.removesuffix(".meta.json")
                if not isinstance(sid, str) or not SESSION_ID.fullmatch(sid):
                    continue
                try:
                    meta = self.metadata(sid)
                except DetailError:
                    continue
                created = timestamp(meta.get("created_at"))
                if end is not None and created is not None and created > end:
                    continue
                candidates.append(meta)
        candidates.sort(key=lambda meta: meta.get("created_at", ""))
        if len(candidates) == 1:
            return candidates, "inferred", candidates[0]["id"]
        # Match legacy attempts by the nearest preceding user turn containing
        # the exact head SHA. Label this as inferred even when the match is unique.
        scored = []
        head = self.activity.get("head_sha")
        if end is not None and head:
            for meta in candidates:
                try:
                    messages, _, _, _ = self.read_messages(meta["id"], meta, "live")
                except DetailError:
                    continue
                times = [timestamp(row.get("created_at")) for row in messages
                         if row.get("role") == "user" and head in str(row.get("content", ""))]
                times = [value for value in times if value is not None and value <= end]
                if times:
                    scored.append((max(times), meta["id"]))
        scored.sort(reverse=True)
        if scored and (len(scored) == 1 or scored[0][0] > scored[1][0]):
            return candidates, "inferred", scored[0][1]
        return candidates, "ambiguous" if candidates else "missing", ""

    def select(self, requested=""):
        candidates, association, preferred = self.candidates()
        sid = requested or preferred
        if not sid:
            return None, candidates, association
        meta = next((meta for meta in candidates if meta["id"] == sid), None)
        if not meta:
            raise DetailError("不能访问其他 PR 的会话", 403)
        return meta, candidates, association

    def sources(self, sid):
        result = [{"id": "live", "label": "当前保存的会话"}]
        directory = self.sessions / ".backups" / sid
        if directory.exists():
            for path in sorted(directory.glob("backup-*.json")):
                if not re.fullmatch(r"backup-[0-9]+", path.stem):
                    continue
                # Resolve before advertising a backup, too.
                try:
                    path.resolve(strict=True).relative_to(self.sessions.resolve())
                except (ValueError, OSError, RuntimeError):
                    continue
                result.append({"id": path.stem, "label": "压缩前备份 " + path.stem.removeprefix("backup-")})
        return result

    def read_messages(self, sid, meta, source):
        warnings, embedded = [], {}
        if source != "live":
            if source not in {item["id"] for item in self.sources(sid)}:
                raise DetailError("会话备份不存在", 404)
            raw = read_inside(self.sessions, f".backups/{sid}/{source}.json")
            data = decode_json(raw)
            if data.get("session_id") != sid or data.get("title") not in (self.title, self.title + ":fix"):
                raise DetailError("备份与当前会话不匹配", 403)
            messages, embedded = data.get("messages", []), data.get("blobs", {})
        elif meta.get("format") == "segment_v1" or (self.sessions / sid / "messages.jsonl").exists():
            raw = read_inside(self.sessions, f"{sid}/messages.jsonl")
            messages = []
            lines = raw.splitlines(keepends=True)
            for index, line in enumerate(lines):
                if not line.strip():
                    continue
                try:
                    messages.append(decode_json(line))
                except DetailError:
                    if index == len(lines) - 1 and not line.endswith(b"\n"):
                        warnings.append("会话正在写入，未完成的最后一条消息暂不显示。")
                        break
                    raise
        else:
            raw = read_inside(self.sessions, sid + ".md")
            match = re.search(rb"```json\s*\n(.*)\n```", raw, re.S)
            if not match:
                raise DetailError("旧会话缺少 JSON 记录", 422)
            data = decode_json(match.group(1))
            if data.get("id") != sid or data.get("title") not in (self.title, self.title + ":fix"):
                raise DetailError("旧会话与当前 PR 不匹配", 403)
            messages = data.get("messages", [])
        if not isinstance(messages, list) or any(not isinstance(row, dict) for row in messages):
            raise DetailError("会话消息格式不支持", 422)
        if any(row.get("name") == "compact_boundary" for row in messages):
            warnings.append("当前记录包含上下文压缩摘要；可切换压缩前备份查看保留的原文。没有备份的已删除内容无法恢复。")
        version = hashlib.sha256(raw).hexdigest()
        return messages, version, warnings, embedded

    def context(self, sid):
        key = hashlib.sha256(self.activity["id"].encode()).hexdigest()
        try:
            data = decode_json(read_inside(self.data, f"review_context/{key}.json"))
        except DetailError as error:
            if error.status == 404:
                return None
            raise
        if (data.get("attempt_id") != self.activity["id"] or data.get("repo") != self.repo
                or data.get("number") != self.number or data.get("session_id") != sid):
            raise DetailError("上下文快照与该次审查不匹配", 403)
        return scrub(self.manager, data)

    def blocks(self, sid, row, embedded):
        row = dict(row)
        if row.get("blob_hash"):
            digest = row["blob_hash"]
            if not isinstance(digest, str) or not BLOB.fullmatch(digest):
                raise DetailError("会话正文引用无效", 422)
            try:
                if digest in embedded:
                    body = base64.b64decode(embedded[digest], validate=True)
                else:
                    body = read_inside(self.sessions, f"{sid}/blobs/{digest}")
                if hashlib.sha256(body).hexdigest() != digest:
                    raise DetailError("会话正文校验不一致", 422)
                row["content"] = body.decode("utf-8")
            except (ValueError, UnicodeError):
                raise DetailError("会话正文无法解码", 422) from None
        blocks = {}
        for key in BLOCKS[:-1]:
            value = row.get(key)
            if value is not None and value != "" and value != []:
                blocks[key] = value if isinstance(value, str) else json.dumps(value, ensure_ascii=False, indent=2)
        extra = {key: value for key, value in row.items() if key not in BLOCKS[:-1]}
        blocks["metadata"] = json.dumps(extra, ensure_ascii=False, indent=2)
        return scrub(self.manager, blocks)

    def transcript(self, sid="", source="live", offset=0, limit=30, version=""):
        meta, candidates, association = self.select(sid)
        result = {"association": association,
                  "candidates": [{key: item.get(key) for key in ("id", "title", "created_at", "updated_at", "message_count")} for item in candidates],
                  "session_id": meta["id"] if meta else "", "messages": [], "total": 0,
                  "scope_note": "这是运行时保存的会话历史，不是每一轮模型请求的完整网络报文。"}
        if not meta:
            return result
        sid = meta["id"]
        rows, current, warnings, embedded = self.read_messages(sid, meta, source)
        if version and version != current:
            raise DetailError("会话内容已更新，请重新加载后继续查看", 409)
        if offset < 0 or offset > len(rows) or not 1 <= limit <= 100:
            raise DetailError("会话分页范围无效")
        result.update(source=source, sources=self.sources(sid), version=current, total=len(rows), offset=offset,
                      warnings=warnings, context=self.context(sid),
                      agent_system_prompt=scrub(self.manager, meta.get("agent_system_prompt", "")))
        for index in range(offset, min(len(rows), offset + limit)):
            row = rows[index]
            item = {"index": index, "role": row.get("role", "unknown"), "name": row.get("name", ""),
                    "created_at": row.get("created_at"), "blocks": []}
            try:
                blocks = self.blocks(sid, row, embedded)
                item["blocks"] = [{"key": key, "text": value[:PREVIEW], "next_offset": min(len(value), PREVIEW), "total": len(value), "has_more": len(value) > PREVIEW} for key, value in blocks.items()]
            except DetailError as error:
                item["error"] = str(error)
                # Show the stored preview explicitly, never pass it off as full content.
                item["blocks"] = [{"key": "content", "text": scrub(self.manager, str(row.get("content", "")))[:PREVIEW], "total": 0, "has_more": False}]
            result["messages"].append(scrub(self.manager, item))
        result["next_offset"] = offset + len(result["messages"])
        result["has_more"] = result["next_offset"] < len(rows)
        return result

    def message_block(self, sid, source, version, index, field, offset):
        if not version or field not in BLOCKS or offset < 0:
            raise DetailError("消息读取参数无效")
        meta, _, _ = self.select(sid)
        if not meta:
            raise DetailError("未找到对应会话", 404)
        rows, current, _, embedded = self.read_messages(sid, meta, source)
        if current != version:
            raise DetailError("会话内容已更新，请重新加载", 409)
        if not 0 <= index < len(rows):
            raise DetailError("消息不存在", 404)
        text = self.blocks(sid, rows[index], embedded).get(field, "")
        if offset > len(text):
            raise DetailError("消息读取范围无效")
        chunk = text[offset:offset + 65536]
        return {"text": chunk, "next_offset": offset + len(chunk), "total": len(text), "has_more": offset + len(chunk) < len(text)}

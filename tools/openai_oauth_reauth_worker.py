#!/usr/bin/env python3
"""Run queued sub2api OpenAI OAuth re-login jobs.

Email-link jobs use Turb's HTTP protocol flow. Password/TOTP jobs use a local
toSub2 checkout selected by TOSUB2_ROOT. The worker never starts a browser.
"""

from __future__ import annotations

import argparse
import base64
import codecs
import multiprocessing
import uuid
import html
import ipaddress
import json
import logging
import os
import re
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
from dataclasses import dataclass, replace
from datetime import datetime, timezone
from html.parser import HTMLParser
from pathlib import Path
from types import SimpleNamespace
from typing import Any, Callable
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import HTTPRedirectHandler, Request, build_opener


LOGGER = logging.getLogger("openai_reauth_worker")
MAX_RESPONSE_BYTES = 1_048_576
CODE_RE = re.compile(r"\b(\d{6})\b")
URL_RE = re.compile(r"https?://[^\s]+", re.IGNORECASE)
SENSITIVE_PARAM_RE = re.compile(
    r"(?i)(code|state|key|token|secret|password)=([^&\s]+)"
)
SENSITIVE_JSON_RE = re.compile(
    r'''(?i)(["']?(?:access_token|refresh_token|id_token|token|code|state|key|secret|password)["']?\s*:\s*)["']?[^"'\s,}]+'''
)
EMAIL_RE = re.compile(r"(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b")
JWT_RE = re.compile(r"\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]*)?\b")
BEARER_RE = re.compile(r"(?i)\bBearer\s+\S+")
CHATGPT_SESSION_COOKIE = "__Secure-next-auth.session-token"
CONTEXT_CODE_RE = re.compile(
    r"(?i)(?:verification|verify|login|temporary|验证码|登录代码|認証|code)"
    r"[^0-9]{0,80}(\d{6})"
)


class WorkerError(RuntimeError):
    pass


class ProtocolHTTPError(WorkerError):
    def __init__(self, stage: str, status_code: int, url: str):
        self.status_code = status_code
        self.endpoint = _safe_openai_endpoint(url)
        super().__init__(f"{stage} returned HTTP {status_code} at {self.endpoint}")


class _NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


NO_REDIRECT_OPENER = build_opener(_NoRedirect)


def _safe_openai_endpoint(url: str) -> str:
    try:
        parsed = urlparse(str(url or ""))
    except ValueError:
        return "[redacted endpoint]"
    if (
        parsed.scheme != "https"
        or (parsed.hostname or "").lower() not in {"auth.openai.com", "sentinel.openai.com", "chatgpt.com"}
        or parsed.port not in (None, 443)
        or parsed.username
        or parsed.password
    ):
        return "[redacted endpoint]"
    return f"{parsed.hostname.lower()}{(parsed.path or '/')[:160]}"


def _checked_bootstrap_authorize(
    session: Any,
    bootstrap: Callable[..., Any],
    state: str,
    *,
    auth_url: str,
) -> None:
    original_get = session.get

    def checked_get(url: str, *args: Any, **kwargs: Any) -> Any:
        response = original_get(url, *args, **kwargs)
        status_code = int(getattr(response, "status_code", 0) or 0)
        if status_code >= 400:
            raise ProtocolHTTPError(
                "OpenAI OAuth bootstrap",
                status_code,
                str(getattr(response, "url", "") or url),
            )
        return response

    session.get = checked_get
    try:
        bootstrap(session, state, auth_url=auth_url)
    finally:
        session.get = original_get


class _PasswordlessIntentParser(HTMLParser):
    def __init__(self) -> None:
        super().__init__()
        self.found = False

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        values = {key.lower(): value or "" for key, value in attrs}
        if tag in {"button", "input"} and values.get("name") == "intent" and values.get("value") == "passwordless_login_send_otp":
            self.found = True


def _has_passwordless_intent(html_body: str) -> bool:
    parser = _PasswordlessIntentParser()
    parser.feed(html_body or "")
    return parser.found


def _start_passwordless_login(session: Any, continue_url: str) -> str:
    parsed = urlparse(continue_url)
    if (
        parsed.scheme != "https"
        or parsed.hostname != "auth.openai.com"
        or parsed.port not in (None, 443)
        or parsed.username
        or parsed.password
    ):
        raise WorkerError("OpenAI passwordless login URL is invalid")
    response = session.get(
        continue_url,
        headers=session.get_auth_navigate_headers(referer="https://auth.openai.com/log-in"),
        allow_redirects=True,
    )
    if response.status_code >= 400:
        raise WorkerError(f"OpenAI password page returned HTTP {response.status_code}")
    page_url = str(getattr(response, "url", "") or continue_url)
    page = urlparse(page_url)
    if page.scheme != "https" or page.hostname != "auth.openai.com" or page.port not in (None, 443):
        raise WorkerError("OpenAI password page redirected to an invalid URL")
    if not _has_passwordless_intent(response.text or ""):
        raise WorkerError("OpenAI passwordless login intent was not found")

    headers = session.get_auth_headers(referer=page_url)
    headers["accept"] = "application/json"
    headers["content-type"] = "application/json"
    result = session.post(
        "https://auth.openai.com/api/accounts/passwordless/send-otp",
        headers=headers,
        allow_redirects=False,
    )
    if result.status_code >= 400:
        raise WorkerError(f"OpenAI passwordless OTP request returned HTTP {result.status_code}")
    try:
        payload = result.json()
    except ValueError:
        raise WorkerError("OpenAI passwordless OTP response was not JSON") from None
    page_type = str((payload.get("page") or {}).get("type") or "") if isinstance(payload, dict) else ""
    if page_type not in {"email_otp_verification", "email_otp_verification_registration"}:
        safe_page_type = re.sub(r"[^A-Za-z0-9_-]", "", page_type)[:80] or "unknown"
        raise WorkerError(f"OpenAI passwordless OTP returned an unexpected page ({safe_page_type})")
    LOGGER.info("passwordless OTP requested status=%s page=%s", result.status_code, page_type)
    return "/email-verification"


def _positive_float(raw: str | None, default: float) -> float:
    try:
        value = float(str(raw or "").strip())
    except ValueError:
        return default
    return value if value > 0 else default


def _trusted_otp_hosts() -> set[str]:
    return {
        host.strip().lower().rstrip(".")
        for host in os.getenv("OPENAI_REAUTH_TRUSTED_OTP_HOSTS", "").split(",")
        if host.strip()
    }


def validate_otp_url(url: str) -> None:
    parsed = urlparse(url)
    host = (parsed.hostname or "").lower().rstrip(".")
    if parsed.scheme not in {"http", "https"} or not host or parsed.username or parsed.password or parsed.fragment:
        raise WorkerError("OTP mailbox URL is invalid")
    loopback_name = host == "localhost"
    if parsed.scheme == "http" and not loopback_name:
        try:
            loopback_name = ipaddress.ip_address(host).is_loopback
        except ValueError:
            loopback_name = False
    if parsed.scheme == "http" and not loopback_name:
        raise WorkerError("OTP mailbox URL must use HTTPS")
    try:
        addresses = {item[4][0] for item in socket.getaddrinfo(host, parsed.port or (443 if parsed.scheme == "https" else 80))}
    except OSError:
        raise WorkerError("OTP mailbox hostname could not be resolved") from None
    trusted_private_host = host in _trusted_otp_hosts()
    for address in addresses:
        ip = ipaddress.ip_address(address)
        if loopback_name and ip.is_loopback:
            continue
        if not ip.is_global and not trusted_private_host:
            raise WorkerError("OTP mailbox hostname resolves to a non-public address")


@dataclass(frozen=True)
class WorkerConfig:
    base_url: str
    worker_token: str
    worker_id: str
    protocol_root: Path | None
    tosub2_root: Path | None = None
    poll_seconds: float = 5.0
    request_timeout: float = 30.0
    concurrency: int = 3

    @classmethod
    def from_env(cls) -> "WorkerConfig":
        base_url = os.getenv("SUB2API_BASE_URL", "http://127.0.0.1:8080").strip().rstrip("/")
        parsed = urlparse(base_url)
        host = (parsed.hostname or "").lower()
        if parsed.scheme not in {"http", "https"} or not host or parsed.username or parsed.password or parsed.query or parsed.fragment:
            raise WorkerError("SUB2API_BASE_URL must be an absolute HTTP(S) URL")
        if parsed.scheme == "http" and host not in {"localhost", "127.0.0.1", "::1"}:
            raise WorkerError("SUB2API_BASE_URL must use HTTPS unless it targets localhost")

        worker_token = os.getenv("OPENAI_REAUTH_WORKER_TOKEN", "").strip()
        if len(worker_token) < 32:
            raise WorkerError("OPENAI_REAUTH_WORKER_TOKEN must contain at least 32 characters")

        worker_id = os.getenv("OPENAI_REAUTH_WORKER_ID", "").strip()
        if not worker_id:
            worker_id = f"{socket.gethostname()}-{os.getpid()}"
        if len(worker_id) > 128:
            raise WorkerError("OPENAI_REAUTH_WORKER_ID must be at most 128 characters")

        root_raw = (os.getenv("CODEX_PROTOCOL_ROOT", "").strip() or os.getenv("TURB_ROOT", "").strip())
        protocol_root = Path(root_raw).expanduser().resolve() if root_raw else None
        if protocol_root is not None:
            required = (
                protocol_root / "core" / "codex_oauth.py",
                protocol_root / "core" / "session.py",
                protocol_root / "sentinel" / "sentinel-runner.js",
                protocol_root / "sentinel" / "sdk.js",
            )
            if not all(path.is_file() for path in required):
                raise WorkerError(
                    "CODEX_PROTOCOL_ROOT/TURB_ROOT must point to a Turb project with the pure protocol and Sentinel assets"
                )

        tosub2_raw = os.getenv("TOSUB2_ROOT", "").strip()
        tosub2_root = Path(tosub2_raw).expanduser().resolve() if tosub2_raw else None
        if tosub2_root is not None:
            required_tosub2 = (
                tosub2_root / "src" / "protocol-login.mjs",
                tosub2_root / "src" / "tls-transport.mjs",
            )
            if not all(path.is_file() for path in required_tosub2):
                raise WorkerError("TOSUB2_ROOT must point to a toSub2 checkout containing src/protocol-login.mjs")

        if protocol_root is None and tosub2_root is None:
            raise WorkerError("configure TOSUB2_ROOT for password/TOTP or CODEX_PROTOCOL_ROOT/TURB_ROOT for email OTP")

        try:
            concurrency = int(os.getenv("OPENAI_REAUTH_CONCURRENCY", "3"))
        except ValueError:
            raise WorkerError("OPENAI_REAUTH_CONCURRENCY must be an integer from 1 to 16") from None
        if not 1 <= concurrency <= 16:
            raise WorkerError("OPENAI_REAUTH_CONCURRENCY must be an integer from 1 to 16")

        return cls(
            base_url=base_url,
            worker_token=worker_token,
            worker_id=worker_id,
            protocol_root=protocol_root,
            tosub2_root=tosub2_root,
            poll_seconds=_positive_float(os.getenv("OPENAI_REAUTH_POLL_SECONDS"), 5.0),
            request_timeout=_positive_float(os.getenv("OPENAI_REAUTH_REQUEST_TIMEOUT"), 30.0),
            concurrency=concurrency,
        )


class WorkerAPI:
    def __init__(self, config: WorkerConfig):
        self.config = config

    def _post(self, path: str, payload: dict[str, Any]) -> Any:
        request = Request(
            self.config.base_url + path,
            data=json.dumps(payload).encode("utf-8"),
            method="POST",
            headers={
                "Content-Type": "application/json",
                "Accept": "application/json",
                "X-OpenAI-Reauth-Worker-Token": self.config.worker_token,
            },
        )
        try:
            with NO_REDIRECT_OPENER.open(request, timeout=self.config.request_timeout) as response:
                body = response.read(MAX_RESPONSE_BYTES + 1)
        except HTTPError as exc:
            message = ""
            try:
                parsed = json.loads(exc.read(MAX_RESPONSE_BYTES).decode("utf-8", errors="replace"))
                message = str(parsed.get("message") or parsed.get("reason") or "")
            except (ValueError, AttributeError):
                pass
            raise WorkerError(f"sub2api HTTP {exc.code}: {message or 'request failed'}") from None
        except (URLError, TimeoutError, OSError) as exc:
            raise WorkerError(f"sub2api request failed: {type(exc).__name__}") from None
        if len(body) > MAX_RESPONSE_BYTES:
            raise WorkerError("sub2api response is too large")
        try:
            envelope = json.loads(body.decode("utf-8"))
        except (UnicodeDecodeError, ValueError):
            raise WorkerError("sub2api returned invalid JSON") from None
        if not isinstance(envelope, dict) or envelope.get("code") != 0:
            raise WorkerError(str(envelope.get("message") or "sub2api request failed"))
        return envelope.get("data")

    def runtime_concurrency(self) -> int:
        data = self._post("/api/v1/internal/openai-reauth/runtime-settings", {})
        value = data.get("worker_concurrency") if isinstance(data, dict) else None
        if isinstance(data, dict) and "worker_concurrency" in data and value is None:
            return self.config.concurrency
        if type(value) is not int or not 1 <= value <= 16:
            raise WorkerError("invalid worker concurrency from server")
        return value

    def totp_phase(self, task_id, phase, secret=""):
        return self._post(f"/api/v1/internal/openai-reauth/{task_id}/totp-phase",
                          {"worker_id": self.config.worker_id, "phase": phase, "secret": secret})

    def totp_finish(self, task_id, success, error_code=""):
        return self._post(f"/api/v1/internal/openai-reauth/{task_id}/totp-finish",
                          {"worker_id": self.config.worker_id, "success": success, "error_code": error_code})

    def totp_recover(self, task_id, secret):
        return self._post(f"/api/v1/internal/openai-reauth/{task_id}/totp-recover", {"secret": secret})

    def claim_totp(self):
        if self.config.tosub2_root is None or not os.getenv("OPENAI_TOTP_JOURNAL_DIR"):
            return None
        try:
            from .openai_totp_rotation import Journal
        except ImportError:
            from openai_totp_rotation import Journal
        journal = Journal()
        if time.monotonic() >= getattr(self, "_next_totp_recovery", 0):
            journal.recover(self)
            self._next_totp_recovery = time.monotonic() + 60
        return self._post("/api/v1/internal/openai-reauth/totp-claim", {"worker_id": self.config.worker_id})

    def claim(self) -> dict[str, Any] | None:
        data = self._post(
            "/api/v1/internal/openai-reauth/claim",
            {"worker_id": self.config.worker_id, "engines": ["local_worker", "session_studio"]},
        )
        return data if isinstance(data, dict) else None

    def claim_session_export(self) -> dict[str, Any] | None:
        data = self._post(
            "/api/v1/internal/openai-reauth/session-export/claim",
            {"worker_id": self.config.worker_id, "capabilities": ["chatgpt_session_export"]},
        )
        return data if isinstance(data, dict) else None

    def session_export_progress(self, task_id: int, stage: str) -> None:
        self._post(
            f"/api/v1/internal/openai-reauth/{task_id}/session-export/progress",
            {"worker_id": self.config.worker_id, "stage": stage},
        )

    def session_export_result(self, task_id: int, session: dict[str, Any]) -> None:
        self._post(
            f"/api/v1/internal/openai-reauth/{task_id}/session-export/result",
            {"worker_id": self.config.worker_id, "session": session},
        )

    def session_export_fail(self, task_id: int, code: str) -> None:
        self._post(
            f"/api/v1/internal/openai-reauth/{task_id}/session-export/fail",
            {"worker_id": self.config.worker_id, "code": code},
        )

    def progress(self, task_id: int, stage: str) -> None:
        self._post(
            f"/api/v1/internal/openai-reauth/{task_id}/progress",
            {"worker_id": self.config.worker_id, "stage": stage},
        )

    def callback(self, task_id: int, callback_url: str) -> dict[str, Any] | None:
        data = self._post(
            f"/api/v1/internal/openai-reauth/{task_id}/callback",
            {"worker_id": self.config.worker_id, "callback_url": callback_url},
        )
        return data if isinstance(data, dict) else None

    def credentials(
        self,
        task_id: int,
        credentials: dict[str, Any],
        extra: dict[str, Any] | None = None,
    ) -> dict[str, Any] | None:
        data = self._post(
            f"/api/v1/internal/openai-reauth/{task_id}/credentials",
            {
                "worker_id": self.config.worker_id,
                "credentials": credentials,
                "extra": extra or {},
            },
        )
        return data if isinstance(data, dict) else None

    def fail(self, task_id: int, reason: str) -> None:
        self._post(
            f"/api/v1/internal/openai-reauth/{task_id}/fail",
            {"worker_id": self.config.worker_id, "reason": sanitize_error(reason)},
        )


def _parse_timestamp(value: Any) -> float | None:
    if isinstance(value, (int, float)):
        timestamp = float(value)
        return timestamp / 1000 if timestamp > 10_000_000_000 else timestamp
    raw = str(value or "").strip()
    if not raw:
        return None
    try:
        timestamp = float(raw)
        return timestamp / 1000 if timestamp > 10_000_000_000 else timestamp
    except ValueError:
        pass
    try:
        return datetime.fromisoformat(raw.replace("Z", "+00:00")).astimezone(timezone.utc).timestamp()
    except ValueError:
        return None


def _structured_otp(value: Any, after_ts: float | None) -> str:
    if isinstance(value, list):
        for item in reversed(value):
            code = _structured_otp(item, after_ts)
            if code:
                return code
        return ""
    if not isinstance(value, dict):
        return ""

    timestamp = next(
        (
            _parse_timestamp(value.get(key))
            for key in (
                "time",
                "date",
                "received_at",
                "receivedAt",
                "created_at",
                "createdAt",
                "timestamp",
            )
            if value.get(key) is not None
        ),
        None,
    )
    if after_ts and timestamp and timestamp + 2 < after_ts:
        return ""

    for key in ("code", "otp", "verification_code", "verificationCode", "email_code", "emailCode"):
        match = CODE_RE.search(str(value.get(key) or ""))
        if match:
            return match.group(1)

    for nested in value.values():
        code = _structured_otp(nested, after_ts)
        if code:
            return code
    return ""


def extract_otp(body: str, after_ts: float | None = None) -> str:
    try:
        parsed = json.loads(body)
        code = _structured_otp(parsed, after_ts)
        if code:
            return code
        if after_ts and isinstance(parsed, dict):
            has_timestamp = any(
                parsed.get(key) is not None
                for key in ("time", "date", "received_at", "receivedAt", "created_at", "createdAt", "timestamp")
            )
            has_code = any(parsed.get(key) is not None for key in ("code", "otp", "verification_code", "verificationCode", "email_code", "emailCode"))
            if has_timestamp and has_code:
                return ""
    except ValueError:
        pass

    text = re.sub(r"(?is)<(?:style|script)[^>]*>.*?</(?:style|script)>", " ", body or "")
    text = html.unescape(re.sub(r"(?s)<[^>]+>", " ", text))
    contextual = CONTEXT_CODE_RE.findall(text)
    if contextual:
        return contextual[-1]
    candidates = [code for code in CODE_RE.findall(text) if code not in {"000000", "202123", "353740"}]
    return candidates[-1] if candidates else ""


class OTPURLProvider:
    def __init__(
        self,
        url: str,
        timeout: float = 20.0,
        on_wait: Callable[[], None] | None = None,
    ):
        validate_otp_url(url)
        self.url = url
        self.timeout = timeout
        self.on_wait = on_wait
        self._announced = False

    def __call__(self, _email: str, after_ts: float | None = None) -> str:
        if self.on_wait and not self._announced:
            self._announced = True
            self.on_wait()
        validate_otp_url(self.url)
        request = Request(
            self.url,
            headers={
                "Accept": "application/json,text/html,text/plain,*/*",
                "User-Agent": "sub2api-openai-reauth-worker/1.0",
            },
        )
        try:
            with NO_REDIRECT_OPENER.open(request, timeout=self.timeout) as response:
                body = response.read(MAX_RESPONSE_BYTES + 1)
        except HTTPError as exc:
            raise WorkerError(f"OTP mailbox returned HTTP {exc.code}") from None
        except (URLError, TimeoutError, OSError) as exc:
            raise WorkerError(f"OTP mailbox request failed: {type(exc).__name__}") from None
        if len(body) > MAX_RESPONSE_BYTES:
            raise WorkerError("OTP mailbox response is too large")
        return extract_otp(body.decode("utf-8", errors="replace"), after_ts)


def load_protocol(root: Path) -> SimpleNamespace:
    node_executable = os.getenv("NODE_EXECUTABLE", "node").strip()
    if not node_executable or shutil.which(node_executable) is None:
        raise WorkerError("Node.js is required for the protocol Sentinel runner")

    root_text = str(root)
    if root_text not in sys.path:
        sys.path.insert(0, root_text)

    # Turb error logs can include provider response bodies. Suppress every
    # Turb log level before importing or executing its protocol modules.
    quiet_level = logging.CRITICAL + 1
    logging.getLogger("core").setLevel(quiet_level)
    logging.getLogger("config").setLevel(quiet_level)

    from core.codex_oauth import (
        _bootstrap_authorize,
        _extract_code,
        _follow_until_callback,
        _is_redirect_uri,
        _post_json,
        _resp_json,
        _select_workspace_and_get_callback,
        build_sentinel_header,
        network_preflight,
        request_sentinel_token,
    )
    from core.openai_auth import (
        AccountUnusableError,
        _extract_error_code,
        detect_account_unusable_response_body,
        send_email_otp,
    )
    # Turb keeps this class name for historical reasons; it is a curl_cffi
    # HTTP session and never launches a browser.
    from core.session import BrowserSession as ProtocolSession

    def submit_email(session: Any, email: str) -> dict[str, Any]:
        sentinel = request_sentinel_token(session, "authorize_continue")
        sentinel_header, so_header = build_sentinel_header(session, sentinel, "authorize_continue")
        response = _post_json(
            session,
            "https://auth.openai.com/api/accounts/authorize/continue",
            {"username": {"kind": "email", "value": email}},
            referer="https://auth.openai.com/log-in",
            sentinel_header=sentinel_header,
            so_header=so_header,
        )
        if response.status_code not in (200, 204):
            raise WorkerError(f"OpenAI email submission returned HTTP {response.status_code}")
        try:
            payload = response.json()
        except ValueError:
            return {}
        return payload if isinstance(payload, dict) else {}

    def submit_email_otp(session: Any, code: str) -> None:
        sentinel = request_sentinel_token(session, "authorize_continue")
        sentinel_header, so_header = build_sentinel_header(session, sentinel, "authorize_continue")
        response = _post_json(
            session,
            "https://auth.openai.com/api/accounts/email-otp/validate",
            {"code": code},
            referer="https://auth.openai.com/email-verification",
            sentinel_header=sentinel_header,
            so_header=so_header,
        )
        if response.status_code != 200:
            error_code = _extract_error_code(response)
            if error_code in ("account_deactivated", "account_deleted", "account_banned"):
                raise AccountUnusableError(
                    f"[Codex] 账号已废（{error_code}）status={response.status_code}: {(response.text or '')[:200]}",
                    error_code=error_code,
                )
            body_error_code = detect_account_unusable_response_body(response.text or "")
            if body_error_code:
                raise AccountUnusableError(
                    f"[Codex] 账号已废（{body_error_code}）status={response.status_code}: {(response.text or '')[:200]}",
                    error_code=body_error_code,
                )
            raise RuntimeError(
                f"[Codex] 邮箱 OTP 验证失败 status={response.status_code}: {(response.text or '')[:300]}"
            )
        # OpenAI can return workspaces in the OTP JSON without setting the
        # oai-client-auth-session cookie expected by Turb's next step.
        workspace_id = _workspace_id_from_auth_payload(_resp_json(response))
        if workspace_id:
            session._sub2api_workspace_id = workspace_id

    def select_workspace_and_get_callback(session: Any, state: str) -> str:
        workspace_id = str(getattr(session, "_sub2api_workspace_id", "") or "").strip()
        if not workspace_id:
            return _select_workspace_and_get_callback(session, state)
        response = _post_json(
            session,
            "https://auth.openai.com/api/accounts/workspace/select",
            {"workspace_id": workspace_id},
            referer="https://auth.openai.com/sign-in-with-chatgpt/codex/consent",
        )
        location = response.headers.get("location") or response.headers.get("Location")
        if location and _is_redirect_uri(location):
            return location
        data = _resp_json(response)
        next_url = ""
        for key in ("redirect_url", "continue_url", "url", "next", "location"):
            value = data.get(key)
            if isinstance(value, str) and value:
                next_url = value
                break
        if not next_url and location:
            next_url = location
        if not next_url:
            raise RuntimeError(
                f"[Codex] workspace/select 后找不到下一跳 URL: status={response.status_code}, "
                f"body={(response.text or '')[:300]}"
            )
        return _follow_until_callback(session, next_url, state)

    return SimpleNamespace(
        ProtocolSession=ProtocolSession,
        AccountUnusableError=AccountUnusableError,
        network_preflight=network_preflight,
        bootstrap_authorize=lambda session, state, auth_url=None: _checked_bootstrap_authorize(
            session,
            _bootstrap_authorize,
            state,
            auth_url=str(auth_url or ""),
        ),
        submit_email=submit_email,
        start_passwordless_login=_start_passwordless_login,
        resend_email_otp=send_email_otp,
        submit_email_otp=submit_email_otp,
        select_workspace_and_get_callback=select_workspace_and_get_callback,
        extract_code=_extract_code,
    )


def _best_effort_progress(api: WorkerAPI, task_id: int, stage: str) -> None:
    try:
        api.progress(task_id, stage)
    except WorkerError as exc:
        LOGGER.warning("task=%s progress=%s failed: %s", task_id, stage, sanitize_error(str(exc)))


def sanitize_error(value: str) -> str:
    text = URL_RE.sub("[redacted URL]", str(value or ""))
    text = SENSITIVE_PARAM_RE.sub(lambda match: f"{match.group(1)}=[redacted]", text)
    text = SENSITIVE_JSON_RE.sub(lambda match: f"{match.group(1)}[redacted]", text)
    text = EMAIL_RE.sub("[redacted email]", text)
    text = JWT_RE.sub("[redacted token]", text)
    text = BEARER_RE.sub("Bearer [redacted]", text)
    text = CODE_RE.sub("[redacted OTP]", text)
    text = re.sub(r"(?is)<(?:style|script)[^>]*>.*?</(?:style|script)>", " ", text)
    text = html.unescape(re.sub(r"(?s)<[^>]+>", " ", text))
    text = re.sub(r"\s+", " ", text).strip()
    return (text or "automatic re-login failed")[:500]


def _extract_chatgpt_session_token(cookies: Any) -> str:
    """Return the Web session cookie, joining NextAuth chunked cookies safely."""
    base = ""
    chunks: dict[int, str] = {}
    prefix = CHATGPT_SESSION_COOKIE + "."
    for cookie in cookies if isinstance(cookies, list) else []:
        if not isinstance(cookie, dict):
            continue
        name = str(cookie.get("name") or "")
        value = str(cookie.get("value") or "")
        if name == CHATGPT_SESSION_COOKIE:
            base = value
        elif name.startswith(prefix):
            suffix = name[len(prefix):]
            if suffix.isdigit() and value:
                chunks[int(suffix)] = value
    if chunks:
        indexes = sorted(chunks)
        if indexes != list(range(indexes[-1] + 1)):
            return ""
        return "".join(chunks[index] for index in indexes)
    return base


def _close_protocol_session(session: Any) -> None:
    raw_session = getattr(session, "session", None)
    close = getattr(raw_session, "close", None)
    if callable(close):
        close()


def _auth_session_flow(session: Any) -> dict[str, Any]:
    raw = ""
    try:
        for cookie in session.session.cookies.jar:
            if cookie.name == "oai-client-auth-session":
                raw = str(cookie.value or "")
                break
    except (AttributeError, TypeError):
        return {}
    try:
        encoded = raw.split(".", 1)[0]
        payload = json.loads(base64.urlsafe_b64decode(encoded + "=" * (-len(encoded) % 4)))
    except (ValueError, TypeError):
        return {}
    if not isinstance(payload, dict):
        return {}
    return {
        key: payload[key]
        for key in ("email_verification_mode", "passwordless_disabled", "signup_mode")
        if key in payload
    }


def _workspace_id_from_auth_payload(payload: Any) -> str:
    if not isinstance(payload, dict):
        return ""
    auth_session = payload.get("oai-client-auth-session")
    if not isinstance(auth_session, dict):
        return ""
    workspaces = auth_session.get("workspaces")
    if not isinstance(workspaces, list):
        return ""
    for workspace in workspaces:
        if isinstance(workspace, dict):
            workspace_id = str(workspace.get("id") or "").strip()
            if workspace_id:
                return workspace_id
    return ""


def _email_submission_state(payload: Any) -> tuple[str, str]:
    if not isinstance(payload, dict):
        return "unknown", ""
    page = payload.get("page")
    page = page if isinstance(page, dict) else {}
    page_type = str(page.get("type") or payload.get("page_type") or "unknown")[:80]
    continue_url = str(
        payload.get("continue_url")
        or payload.get("external_url")
        or payload.get("url")
        or page.get("continue_url")
        or page.get("external_url")
        or page.get("url")
        or ""
    )
    try:
        path = urlparse(continue_url).path[:160]
    except ValueError:
        path = ""
    return page_type, path


def _email_submission_url(payload: Any) -> str:
    if not isinstance(payload, dict):
        return ""
    page = payload.get("page")
    page = page if isinstance(page, dict) else {}
    return str(
        payload.get("continue_url")
        or payload.get("external_url")
        or payload.get("url")
        or page.get("continue_url")
        or page.get("external_url")
        or page.get("url")
        or ""
    )


def _extract_state(auth_url: str) -> str:
    parsed = urlparse(auth_url)
    query = parsed.query
    for item in query.split("&"):
        key, separator, value = item.partition("=")
        if separator and key == "state":
            from urllib.parse import unquote_plus

            return unquote_plus(value)
    return ""


def _is_local_codex_callback(url: str) -> bool:
    parsed = urlparse(url)
    return (
        parsed.scheme == "http"
        and parsed.netloc.lower() == "localhost:1455"
        and parsed.path == "/auth/callback"
        and not parsed.params
        and not parsed.fragment
    )


def _wait_for_otp(
    provider: OTPURLProvider,
    email: str,
    after_ts: float,
    *,
    timeout: float,
    interval: float,
    excluded_codes: set[str] | None = None,
) -> str:
    excluded_codes = excluded_codes or set()
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        code = provider(email, after_ts=after_ts)
        if code and code not in excluded_codes:
            return code
        remaining = deadline - time.monotonic()
        if remaining > 0:
            time.sleep(min(interval, remaining))
    raise WorkerError("timed out waiting for a new mailbox verification code")


def process_claim(api: WorkerAPI, protocol: SimpleNamespace | None, claim: dict[str, Any]) -> None:
    mode = str(claim.get("credential_mode") or "email_otp_url").strip()
    if mode == "password_totp":
        process_password_claim(api, claim)
        return
    if mode != "email_otp_url":
        raise WorkerError("claimed task has an unsupported credential mode")
    if protocol is None:
        raise WorkerError("email OTP re-login requires CODEX_PROTOCOL_ROOT/TURB_ROOT")

    task_id = int(claim.get("task_id") or 0)
    account_id = int(claim.get("account_id") or 0)
    email = str(claim.get("login_email") or "").strip()
    otp_url = str(claim.get("otp_url") or "").strip()
    auth_url = str(claim.get("auth_url") or "").strip()
    proxy_url = str(claim.get("proxy_url") or "").strip()
    if task_id <= 0 or account_id <= 0 or not email or not otp_url or not auth_url:
        raise WorkerError("claimed task is missing required fields")

    state = _extract_state(auth_url)
    if not state:
        raise WorkerError("authorization URL is missing OAuth state")

    LOGGER.info("task=%s account=%s started (protocol)", task_id, account_id)
    _best_effort_progress(api, task_id, "starting")
    session = None
    try:
        _best_effort_progress(api, task_id, "protocol_connecting")
        # An empty proxy explicitly means direct connection.  Do not let the
        # Turb project silently choose a local proxy pool for this worker.
        for bootstrap_attempt in (1, 2):
            session = protocol.ProtocolSession(
                proxy=proxy_url or "",
                detect_exit_geo=bool(proxy_url),
                use_exit_geo_cache=False,
            )
            try:
                protocol.network_preflight(session)
                protocol.bootstrap_authorize(session, state, auth_url=auth_url)
                break
            except ProtocolHTTPError as exc:
                _close_protocol_session(session)
                session = None
                if exc.status_code != 403 or bootstrap_attempt == 2:
                    raise
                LOGGER.warning(
                    "task=%s OAuth bootstrap received HTTP 403 at %s; retrying once with a fresh protocol session",
                    task_id,
                    exc.endpoint,
                )
                time.sleep(1.0)
        otp_after_ts = time.time()
        submission = protocol.submit_email(session, email)
        page_type, continue_path = _email_submission_state(submission)
        flow = _auth_session_flow(session)
        LOGGER.info(
            "task=%s auth page=%s path=%s mode=%s passwordless_disabled=%s signup_mode=%s",
            task_id,
            page_type,
            continue_path or "unknown",
            flow.get("email_verification_mode", "unknown"),
            flow.get("passwordless_disabled", "unknown"),
            flow.get("signup_mode", "unknown"),
        )
        if page_type in {"login_password", "login-password", "password_login"} or continue_path == "/log-in/password":
            passwordless_url = _email_submission_url(submission)
            if not passwordless_url:
                raise WorkerError("OpenAI password page response did not include a continue URL")
            otp_after_ts = time.time()
            protocol.start_passwordless_login(session, passwordless_url)
            LOGGER.info("task=%s switched from password login to email OTP", task_id)
        _best_effort_progress(api, task_id, "email_submitted")
        otp_provider = OTPURLProvider(otp_url)
        max_otp_attempts = 3
        attempted_codes: set[str] = set()
        for attempt in range(1, max_otp_attempts + 1):
            try:
                _best_effort_progress(api, task_id, "waiting_otp")
                code = _wait_for_otp(
                    otp_provider,
                    email,
                    otp_after_ts,
                    timeout=180.0,
                    interval=2.0,
                    excluded_codes=attempted_codes,
                )
                attempted_codes.add(code)
                protocol.submit_email_otp(session, code)
                break
            except protocol.AccountUnusableError:
                raise
            except Exception as exc:
                if attempt >= max_otp_attempts:
                    raise
                LOGGER.warning("task=%s OTP attempt %s/%s failed: %s", task_id, attempt, max_otp_attempts, sanitize_error(str(exc)))
                protocol.resend_email_otp(session)
                otp_after_ts = time.time()
                _best_effort_progress(api, task_id, "email_submitted")
        _best_effort_progress(api, task_id, "otp_submitted")
        _best_effort_progress(api, task_id, "waiting_callback")
        callback_url = protocol.select_workspace_and_get_callback(session, state)
        if not _is_local_codex_callback(callback_url):
            raise WorkerError("Codex protocol returned an unexpected callback URL")
        protocol.extract_code(callback_url, state)
        result = api.callback(task_id, callback_url)
        if not result or result.get("status") != "succeeded":
            raise WorkerError(str((result or {}).get("error") or "callback was not accepted"))
        LOGGER.info("task=%s account=%s succeeded", task_id, account_id)
    finally:
        if session is not None:
            try:
                _close_protocol_session(session)
            except Exception as exc:
                LOGGER.warning("task=%s protocol session cleanup failed: %s", task_id, sanitize_error(str(exc)))


def _session_studio_result(response: Any) -> dict[str, Any]:
    """Decode bounded JSON/NDJSON, stopping at the terminal object, not EOF."""
    decoder = json.JSONDecoder()
    utf8 = codecs.getincrementaldecoder("utf-8")()
    pending = ""
    total = 0
    deadline = time.monotonic() + 25 * 60
    while True:
        while pending.strip():
            pending = pending.lstrip()
            try:
                event, end = decoder.raw_decode(pending)
            except ValueError:
                break
            pending = pending[end:]
            if not isinstance(event, dict):
                raise WorkerError("Session Studio returned invalid JSON")
            if event.get("type") == "result":
                payload = event.get("payload")
                if not isinstance(payload, dict):
                    raise WorkerError("Session Studio returned an invalid result")
                return payload
            if "credential" in event or "error" in event or str(event.get("status", "")).lower() in {
                "active", "ok", "success", "succeeded", "failed", "error", "auth_failed"
            }:
                return event
        if time.monotonic() >= deadline:
            raise WorkerError("Session Studio response timed out")
        chunk = response.read1(4096)
        if not chunk:
            raise WorkerError("Session Studio response ended without a result")
        total += len(chunk)
        if total > 8 * MAX_RESPONSE_BYTES:
            raise WorkerError("Session Studio response is too large")
        try:
            pending += utf8.decode(chunk)
        except UnicodeDecodeError:
            raise WorkerError("Session Studio returned invalid JSON") from None


def process_session_studio_claim(api: WorkerAPI, claim: dict[str, Any]) -> None:
    task_id = int(claim.get("task_id") or 0)
    account_id = int(claim.get("account_id") or 0)
    email = str(claim.get("login_email") or "").strip()
    password = str(claim.get("password") or "")
    if task_id <= 0 or account_id <= 0 or not email or not password:
        raise WorkerError("claimed password task is missing required fields")
    # Never infer an external destination or silently fall back to an engine.
    endpoint = str(claim.get("relogin_endpoint") or "").strip()
    try:
        parsed = urlparse(endpoint)
        valid = parsed.scheme == "https" and parsed.hostname and not (
            parsed.username or parsed.password or parsed.query or parsed.fragment
        )
        parsed.port
    except ValueError:
        valid = False
    if not valid:
        raise WorkerError("Session Studio requires a configured HTTPS endpoint")
    if claim.get("proxy_url"):
        raise WorkerError("Session Studio does not accept an account proxy")
    origin = f"https://{parsed.netloc}"
    headers = {
        "Content-Type": "application/json", "Accept": "application/x-ndjson, application/json",
        "Origin": origin, "Referer": origin + "/",
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/131.0",
    }
    extra_headers = claim.get("relogin_headers") or {}
    if not isinstance(extra_headers, dict):
        raise WorkerError("Session Studio headers are invalid")
    reserved = {"host", "content-length", "transfer-encoding", "connection", "x-openai-reauth-worker-token"}
    for name, value in extra_headers.items():
        if (not isinstance(name, str) or not isinstance(value, str)
                or not re.fullmatch(r"[!#$%&'*+.^_\x60|~0-9A-Za-z-]+", name)
                or name.lower() in reserved or "\r" in value or "\n" in value):
            raise WorkerError("Session Studio headers are invalid")
        headers[name] = value.replace("{{uuid}}", str(uuid.uuid4()))
    request = Request(endpoint, method="POST", headers=headers, data=json.dumps({
        "action": "start", "auth_mode": "password_2fa", "email": email,
        "password": password, "mfa_secret": str(claim.get("totp_secret") or ""),
    }).encode("utf-8"))
    LOGGER.info("task=%s account=%s started (session_studio)", task_id, account_id)
    started = time.monotonic()
    _best_effort_progress(api, task_id, "starting")
    _best_effort_progress(api, task_id, "protocol_connecting")
    try:
        # No redirects: credentials must never be forwarded to another target.
        with NO_REDIRECT_OPENER.open(request, timeout=60) as response:
            payload = _session_studio_result(response)
    except HTTPError as exc:
        raise WorkerError(f"Session Studio HTTP {exc.code}") from None
    except (URLError, TimeoutError, OSError, ValueError):
        raise WorkerError("Session Studio request failed") from None
    credentials = payload.get("credential")
    if payload.get("error") or str(payload.get("status", "")).lower() in {"failed", "error", "auth_failed"}:
        # Provider errors may echo secrets. Do not relay their message or code.
        raise WorkerError("Session Studio login failed")
    if not isinstance(credentials, dict) or not all(
        isinstance(credentials.get(key), str) and credentials[key].strip()
        for key in ("access_token", "refresh_token", "id_token")
    ):
        raise WorkerError("Session Studio returned incomplete OAuth credentials")
    _best_effort_progress(api, task_id, "applying_credentials")
    result = api.credentials(task_id, credentials, {})
    if not result or result.get("status") != "succeeded":
        raise WorkerError("Session Studio credentials were not accepted")
    LOGGER.info("task=%s account=%s succeeded engine=session_studio elapsed_ms=%d",
                task_id, account_id, (time.monotonic() - started) * 1000)


def _siwc_password_failure(output: str, exit_code: int) -> str:
    # Report only fixed labels: the runner can echo URLs, passwords and tokens.
    stage = "starting"
    for marker, label in (("[1/5]", "authorize"), ("Password accepted", "password_accepted"),
                          ("TOTP 2FA challenge reached", "mfa_challenge"),
                          ("2FA verification accepted", "mfa_accepted")):
        if marker in output:
            stage = label
    reason = "protocol_error"
    for markers, label in (
        (("Password was rejected",), "password_rejected"),
        (("2FA key was rejected", "configured 2FA key was rejected", "2FA code was rejected"), "totp_rejected"),
        (("SIWC_WORKSPACE_MISMATCH",), "workspace_mismatch"),
        (("SIWC_AUTH_URL_INVALID",), "invalid_auth_url"),
        (("SIWC_CALLBACK_INVALID",), "invalid_callback"),
        (("PROXY_RISK_CONTROL", "security-check", "返回安全校验页面"), "security_check"),
        (("ECONNREFUSED", "ENOTFOUND", "ETIMEDOUT", "fetch failed"), "transport_error"),
        (("ERR_MODULE_NOT_FOUND", "MODULE_NOT_FOUND", "ENOENT", "SyntaxError", "ReferenceError"), "runtime_error"),
        (("SIWC_INTERACTION_REQUIRED",), "browser_interaction_required"),
    ):
        if any(marker in output for marker in markers):
            reason = label
            break
    guidance = "; use browser authorization in account edit" if reason in {"browser_interaction_required", "security_check"} else ""
    return f"SIWC authorization failed (stage={stage}, reason={reason}, exit={exit_code}); credentials retained{guidance}"


def process_password_claim(api: WorkerAPI, claim: dict[str, Any]) -> None:
    oauth_profile = str(claim.get("oauth_profile") or "codex")
    if oauth_profile not in {"codex", "excel", "siwc"}:
        raise WorkerError("unsupported OAuth login profile")
    engine = str(claim.get("engine") or "local_worker")
    if oauth_profile in {"excel", "siwc"} and engine != "local_worker":
        raise WorkerError("Excel OAuth requires the local password/TOTP worker")
    if engine == "session_studio":
        process_session_studio_claim(api, claim)
        return
    if engine != "local_worker":
        raise WorkerError("unsupported password re-login engine")
    task_id = int(claim.get("task_id") or 0)
    account_id = int(claim.get("account_id") or 0)
    email = str(claim.get("login_email") or "").strip()
    password = str(claim.get("password") or "")
    totp_secret = str(claim.get("totp_secret") or "").strip()
    proxy_url = str(claim.get("proxy_url") or "").strip()
    root = getattr(getattr(api, "config", None), "tosub2_root", None)
    if task_id <= 0 or account_id <= 0 or not email or not password:
        raise WorkerError("claimed password task is missing required fields")
    if root is None:
        raise WorkerError("TOSUB2_ROOT is required for password/TOTP re-login")

    node_executable = os.getenv("NODE_EXECUTABLE", "node").strip()
    if not node_executable or shutil.which(node_executable) is None:
        raise WorkerError("Node.js is required for password/TOTP re-login")
    script = root.resolve() / "src" / "protocol-login.mjs"
    if not script.is_file():
        raise WorkerError("toSub2 protocol-login.mjs is unavailable")

    LOGGER.info("task=%s account=%s started (password protocol)", task_id, account_id)
    _best_effort_progress(api, task_id, "starting")
    _best_effort_progress(api, task_id, "protocol_connecting")
    with tempfile.TemporaryDirectory(prefix="sub2api-reauth-") as temp_dir:
        output_path = Path(temp_dir) / "oauth.json"
        if oauth_profile == "excel":
            try:
                from .openai_excel_oauth_adapter import prepare_excel_runtime
            except ImportError:
                from openai_excel_oauth_adapter import prepare_excel_runtime
            try:
                script = prepare_excel_runtime(root.resolve(), Path(temp_dir))
            except ValueError as exc:
                raise WorkerError(str(exc)) from None
        if oauth_profile == "siwc":
            try:
                from .openai_siwc_oauth_adapter import prepare_siwc_runtime
            except ImportError:
                from openai_siwc_oauth_adapter import prepare_siwc_runtime
            try:
                script = prepare_siwc_runtime(root.resolve(), Path(temp_dir))
            except ValueError as exc:
                raise WorkerError(str(exc)) from None
        command = [
            node_executable,
            str(script),
            "--email",
            email,
            "--output-mode",
            "sub2api",
            "--sub2api-out",
            str(output_path),
            "--sub2api-name",
            f"credential-guard-{account_id}",
        ]
        if proxy_url:
            command.extend(("--proxy", proxy_url))
        child_env = os.environ.copy()
        child_env.pop("OPENAI_REAUTH_WORKER_TOKEN", None)
        if oauth_profile == "excel":
            # One bounded fixed-exit attempt. Never rotate proxies to retry a rejected login.
            child_env["CHATGPT_PROXY_MAX_ATTEMPTS"] = "1"
            child_env.pop("CHATGPT_BASE", None)
            child_env.pop("AUTH_BASE", None)
            child_env.pop("CHATGPT_PROXY_URL", None)
            child_env.pop("OPENAI_EXCEL_EXPECTED_WORKSPACE", None)
            if claim.get("expected_workspace"):
                child_env["OPENAI_EXCEL_EXPECTED_WORKSPACE"] = str(claim["expected_workspace"])
        # Ambient credentials and alternate endpoints never select the SIWC flow.
        child_env.pop("OPENAI_SIWC_AUTH_URL", None)
        child_env.pop("OPENAI_SIWC_EXPECTED_WORKSPACE", None)
        if oauth_profile == "siwc":
            child_env["OPENAI_SIWC_AUTH_URL"] = str(claim.get("auth_url") or "")
            child_env["OPENAI_SIWC_EXPECTED_WORKSPACE"] = str(claim.get("expected_workspace") or "")
            for key in ("CHATGPT_BASE", "AUTH_BASE", "CHATGPT_PROXY_URL"):
                child_env.pop(key, None)
            child_env["CHATGPT_PROXY_MAX_ATTEMPTS"] = "1"
        child_env["CHATGPT_LOGIN_PASSWORD"] = password
        if totp_secret:
            child_env["CHATGPT_TOTP_SECRET"] = totp_secret
        else:
            child_env.pop("CHATGPT_TOTP_SECRET", None)
        try:
            result = subprocess.run(
                command,
                cwd=temp_dir,
                env=child_env,
                stdin=subprocess.DEVNULL,
                capture_output=True,
                text=True,
                timeout=10 * 60 if oauth_profile in {"excel", "siwc"} else 25 * 60,
                check=False,
            )
        except subprocess.TimeoutExpired:
            raise WorkerError("password/TOTP protocol timed out") from None
        if oauth_profile == "excel" and callable(getattr(api, "private_protocol_diagnostics", None)):
            # Only the explicit isolated CLI implements this hook. The queue API
            # cannot select a path or persist raw external-runner output.
            api.private_protocol_diagnostics(result.stdout, result.stderr)
        if result.returncode != 0:
            if oauth_profile == "siwc":
                raise WorkerError(_siwc_password_failure(result.stdout + "\n" + result.stderr, result.returncode))
            # The external runner may echo unlabeled secrets that regexes cannot redact.
            if oauth_profile == "excel":
                private_output = result.stdout + "\n" + result.stderr
                stage = "web_login"
                for marker, label in (("Password accepted", "password_accepted"), ("TOTP 2FA challenge reached", "mfa_challenge"),
                                      ("2FA verification accepted", "mfa_accepted"), ("Start Codex OAuth flow", "excel_authorize"),
                                      ("Convert OAuth callback", "excel_token_exchange")):
                    if marker in private_output:
                        stage = label
                reasons = [label for marker, label in (("security-check", "security_check"), ("PROXY_RISK_CONTROL", "security_check"),
                           ("EXCEL_ADDITIONAL_VERIFICATION_REQUIRED", "additional_verification"),
                           ("EXCEL_CALLBACK_STATE_MISMATCH", "callback_state_mismatch"),
                           ("EXCEL_EXPECTED_WORKSPACE_NOT_LISTED", "workspace_not_listed"),
                           ("Password was rejected", "password_rejected"), ("2FA key was rejected", "totp_rejected"),
                           ("SESSION_SELECTION_INVALID", "session_selection"), ("CLIENT_ID", "client_id")) if marker in private_output]
                reason = ",".join(sorted(set(reasons))) or "protocol_error"
                raise WorkerError(f"Excel password/TOTP login failed (stage={stage}, reason={reason}, exit={result.returncode})")
            private_output = result.stdout + "\n" + result.stderr
            if proxy_url and any(marker in private_output.lower() for marker in (
                "econnrefused", "connection refused", "proxy connect", "proxy error",
                "could not connect", "connect etimedout", "socket hang up",
            )):
                raise WorkerError("proxy_unavailable")
            if "Password was rejected" in private_output:
                raise WorkerError("password_rejected")
            if "2FA key was rejected" in private_output or "code generated from the configured 2FA key was rejected" in private_output:
                raise WorkerError("totp_rejected")
            raise WorkerError(f"password/TOTP protocol failed (exit {result.returncode})")
        try:
            payload = json.loads(output_path.read_text(encoding="utf-8"))
        except (OSError, UnicodeDecodeError, ValueError):
            raise WorkerError("password/TOTP protocol did not produce valid OAuth credentials") from None

    if oauth_profile == "siwc":
        callback = payload.get("siwc_callback_url") if isinstance(payload, dict) else None
        if not isinstance(callback, str) or not callback:
            raise WorkerError("SIWC callback was not produced")
        result = api.callback(task_id, callback)
        if not result or result.get("status") != "succeeded":
            raise WorkerError("SIWC callback was not accepted")
        return
    accounts = payload.get("accounts") if isinstance(payload, dict) else None
    account = accounts[0] if isinstance(accounts, list) and len(accounts) == 1 else None
    credentials = account.get("credentials") if isinstance(account, dict) else None
    extra = account.get("extra") if isinstance(account, dict) else None
    if not isinstance(credentials, dict) or not all(
        str(credentials.get(key) or "").strip()
        for key in ("access_token", "refresh_token", "id_token")
    ):
        raise WorkerError("password/TOTP protocol returned incomplete OAuth credentials")
    if oauth_profile == "excel":
        try:
            from .openai_excel_oauth_adapter import validate_excel_credentials
        except ImportError:
            from openai_excel_oauth_adapter import validate_excel_credentials
        try:
            validate_excel_credentials(credentials, email, claim.get("expected_workspace"))
        except ValueError as exc:
            raise WorkerError(str(exc)) from None
    _best_effort_progress(api, task_id, "applying_credentials")
    result = api.credentials(task_id, credentials, extra if isinstance(extra, dict) else {})
    if not result or result.get("status") != "succeeded":
        raise WorkerError(str((result or {}).get("error") or "credentials were not accepted"))
    LOGGER.info("task=%s account=%s succeeded", task_id, account_id)


def process_session_export_claim(api: WorkerAPI, claim: dict[str, Any]) -> None:
    """Login to ChatGPT Web with the saved password/TOTP and return one session payload."""
    task_id = int(claim.get("task_id") or 0)
    if task_id <= 0:
        raise WorkerError("claimed Session export task is missing task id")
    email = str(claim.get("login_email") or "").strip()
    password = str(claim.get("password") or "")
    totp_secret = str(claim.get("totp_secret") or "").strip()
    if not email or not password or not totp_secret:
        raise WorkerError("Session export credentials are incomplete")
    root = getattr(getattr(api, "config", None), "tosub2_root", None)
    if root is None:
        raise WorkerError("TOSUB2_ROOT is required for ChatGPT Session export")
    try:
        from .openai_totp_rotation import WebSession, login_web
    except ImportError:
        from openai_totp_rotation import WebSession, login_web

    _best_effort_session_progress(api, task_id, "starting")
    _best_effort_session_progress(api, task_id, "protocol_connecting")
    session = None
    try:
        login_claim = {
            "login_email": email,
            "password": password,
            "proxy_url": str(claim.get("proxy_url") or ""),
        }
        _best_effort_session_progress(api, task_id, "password_submitted")
        data = login_web(api.config, login_claim, totp_secret)
        if not isinstance(data, dict):
            raise WorkerError("totp_rejected")
        if data.get("web", {}).get("mfaVerified") is not True:
            raise WorkerError("ChatGPT Web login did not verify 2FA")
        _best_effort_session_progress(api, task_id, "mfa_submitted")
        session = WebSession(api.config, login_claim, totp_secret, login=lambda *_args: data)
        _best_effort_session_progress(api, task_id, "session_request")
        # WebSession performs the authenticated GET /api/auth/session while
        # constructing the cookie-backed client. Reuse that complete upstream
        # JSON instead of issuing a second request and rebuilding a reduced
        # three-field object.
        payload = getattr(session, "auth_session_payload", None)
        if not isinstance(payload, dict):
            raise WorkerError("ChatGPT returned an invalid Session")
        user = payload.get("user") if isinstance(payload.get("user"), dict) else {}
        expected_user_id = str(claim.get("expected_user_id") or "").strip()
        if expected_user_id and str(user.get("id") or "").strip() != expected_user_id:
            raise WorkerError("ChatGPT Session identity mismatch")
        if not str(payload.get("accessToken") or "").strip() or not str(payload.get("expires") or "").strip():
            raise WorkerError("ChatGPT returned an incomplete Session")
        session_token = _extract_chatgpt_session_token(data.get("cookies"))
        if not session_token:
            raise WorkerError("ChatGPT returned no session token")
        # Keep upstream fields internally for identity and expiry checks. The
        # API compacts the downloadable result to accessToken/sessionToken.
        payload = {**payload, "sessionToken": session_token}
        _best_effort_session_progress(api, task_id, "identity_verification")
        api.session_export_result(task_id, payload)
    finally:
        if session is not None:
            try:
                session.close()
            except Exception:
                pass


def _best_effort_session_progress(api: WorkerAPI, task_id: int, stage: str) -> None:
    try:
        api.session_export_progress(task_id, stage)
    except WorkerError as exc:
        LOGGER.warning("task=%s Session export progress=%s failed: %s", task_id, stage, sanitize_error(str(exc)))


def session_export_error_code(exc: BaseException) -> str:
    """Collapse protocol/network exceptions to the small API error-code set."""
    text = sanitize_error(str(exc)).lower()
    if "credentials are incomplete" in text:
        return "credentials_required"
    if "no session token" in text:
        return "session_token_missing"
    if "tosub2_root" in text or ("worker" in text and "required" in text):
        return "worker_offline"
    if "proxy_unavailable" in text or "proxy" in text and "unavailable" in text:
        return "proxy_unavailable"
    if "totp" in text or "verification_failed" in text or "2fa" in text or "mfa" in text:
        return "totp_rejected"
    if "password_rejected" in text:
        return "password_rejected"
    if "login_failed" in text:
        return "upstream_rejected"
    if "identity" in text:
        return "identity_mismatch"
    if "timeout" in text:
        return "worker_timeout"
    if "upstream" in text or "response" in text or "http" in text:
        return "upstream_rejected"
    return "worker_failed"


def run_once(api: WorkerAPI, protocol: SimpleNamespace | None) -> bool:
    if callable(getattr(api, "claim_totp", None)):
        try:
            rotation = api.claim_totp()
        except Exception:
            rotation = None  # Older APIs and unavailable recovery storage do not disrupt re-login.
        if rotation:
            try:
                from .openai_totp_rotation import rotate
            except ImportError:
                from openai_totp_rotation import rotate
            rotate(api, rotation)
            return True
    try:
        claim = api.claim()
    except WorkerError as exc:
        LOGGER.warning("claim failed: %s", sanitize_error(str(exc)))
        return False
    if not claim:
        try:
            claim = api.claim_session_export()
        except WorkerError as exc:
            LOGGER.warning("Session export claim failed: %s", sanitize_error(str(exc)))
            return False
        if not claim:
            return False
        task_id = int(claim.get("task_id") or 0)
        try:
            process_session_export_claim(api, claim)
        except Exception as exc:
            reason = session_export_error_code(exc)
            LOGGER.error("Session export task=%s failed: %s", task_id or "?", reason)
            if task_id > 0:
                try:
                    api.session_export_fail(task_id, reason)
                except WorkerError as fail_exc:
                    LOGGER.error("Session export task=%s failure report rejected: %s", task_id, sanitize_error(str(fail_exc)))
        return True
    task_id = int(claim.get("task_id") or 0)
    try:
        process_claim(api, protocol, claim)
    except Exception as exc:
        reason = sanitize_error(f"{type(exc).__name__}: {exc}")
        LOGGER.error("task=%s failed: %s", task_id or "?", reason)
        if task_id > 0:
            try:
                api.fail(task_id, reason)
            except WorkerError as fail_exc:
                LOGGER.error("task=%s failure report rejected: %s", task_id, sanitize_error(str(fail_exc)))
    return True


def terminate_worker(_signum: int, _frame: Any) -> None:
    # The managed launcher makes us a process-group leader. On API death its
    # parent-death SIGTERM must also terminate Node/TLS descendants.
    if hasattr(os, "getpgrp") and os.getpgrp() == os.getpid():
        try:
            os.killpg(os.getpgrp(), signal.SIGKILL)
        except OSError:
            pass
    raise SystemExit(0)


def _worker_loop(config: WorkerConfig, stop: Any = None) -> None:
    signal.signal(signal.SIGTERM, terminate_worker)
    protocol = load_protocol(config.protocol_root) if config.protocol_root is not None else None
    api = WorkerAPI(config)
    LOGGER.info("worker=%s ready", config.worker_id)
    while stop is None or not stop.is_set():
        if not run_once(api, protocol):
            if stop is None:
                time.sleep(config.poll_seconds)
            else:
                stop.wait(config.poll_seconds)


def worker_slot_config(config: WorkerConfig, slot: int) -> WorkerConfig:
    suffix = f"-slot-{slot}"
    # Preserve the existing 128-character worker ownership limit.
    return replace(config, worker_id=config.worker_id[:128-len(suffix)] + suffix, concurrency=1)


def reconcile_worker_pool(context: Any, children: dict, config: WorkerConfig, target: int) -> None:
    # Retiring processes finish their current task before exiting. They do not
    # claim another task. Never replace a retiring slot until it has exited.
    for slot, (child, stop) in list(children.items()):
        if not child.is_alive():
            child.join()
            del children[slot]
            if not stop.is_set():
                raise WorkerError("a re-login worker process exited")
    for slot, (_child, stop) in children.items():
        if slot > target:
            stop.set()
    for slot in range(1, target + 1):
        if slot not in children:
            stop = context.Event()
            child = context.Process(target=_worker_loop, args=(worker_slot_config(config, slot), stop))
            child.start()
            children[slot] = (child, stop)


def run_worker_pool(config: WorkerConfig) -> None:
    context = multiprocessing.get_context("fork" if sys.platform.startswith("linux") else "spawn")
    children = {}
    api = WorkerAPI(config)
    target = config.concurrency
    next_poll = 0.0
    try:
        while True:
            now = time.monotonic()
            if now >= next_poll:
                try:
                    target = api.runtime_concurrency()
                except WorkerError:
                    # Older API versions and temporary outages retain the last
                    # working count; they never interrupt active credentials.
                    pass
                next_poll = now + 5
            reconcile_worker_pool(context, children, config, target)
            time.sleep(0.25)
    finally:
        for child, _stop in children.values():
            if child.is_alive():
                child.terminate()
        for child, _stop in children.values():
            child.join(timeout=5)
            if child.is_alive():
                child.kill()
                child.join()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--once", action="store_true", help="claim at most one task, then exit")
    parser.add_argument("--check", action="store_true", help="validate local configuration without claiming a task")
    args = parser.parse_args()
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    try:
        config = WorkerConfig.from_env()
        node_executable = os.getenv("NODE_EXECUTABLE", "node").strip()
        if not node_executable or shutil.which(node_executable) is None:
            raise WorkerError("Node.js is required for the re-login protocol runner")
        protocol = load_protocol(config.protocol_root) if config.protocol_root is not None else None
        api = WorkerAPI(config)
    except Exception as exc:
        LOGGER.error("worker startup failed: %s", sanitize_error(f"{type(exc).__name__}: {exc}"))
        return 2

    if args.check:
        LOGGER.info("worker configuration valid (password_totp=%s email_otp_url=%s)", config.tosub2_root is not None, protocol is not None)
        return 0
    signal.signal(signal.SIGTERM, terminate_worker)
    LOGGER.info("worker=%s concurrency=%s ready", config.worker_id, config.concurrency)
    try:
        if not args.once:
            run_worker_pool(config)
            return 0
        while True:
            worked = run_once(api, protocol)
            if args.once:
                return 0
            if not worked:
                time.sleep(config.poll_seconds)
    except WorkerError as exc:
        LOGGER.error("worker pool failed: %s", sanitize_error(str(exc)))
        return 1
    except KeyboardInterrupt:
        LOGGER.info("worker stopped")
        return 0


if __name__ == "__main__":
    raise SystemExit(main())

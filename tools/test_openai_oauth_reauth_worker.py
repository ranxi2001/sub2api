import json
import os
import tempfile
import threading
import time
import unittest
import base64
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch
from types import SimpleNamespace

from tools.openai_oauth_reauth_worker import (
    OTPURLProvider,
    ProtocolHTTPError,
    WorkerConfig,
    WorkerError,
    _auth_session_flow,
    _checked_bootstrap_authorize,
    _email_submission_state,
    _has_passwordless_intent,
    _start_passwordless_login,
    _is_local_codex_callback,
    _safe_openai_endpoint,
    _workspace_id_from_auth_payload,
    _siwc_password_failure,
    process_claim,
    process_password_claim,
    _wait_for_otp,
    extract_otp,
    sanitize_error,
    validate_otp_url,
    main,
    terminate_worker,
)


class OTPHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps({"code": "482931", "subject": "ChatGPT login code"}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, _format, *_args):
        pass


class OpenAIOAuthReauthWorkerTest(unittest.TestCase):
    def test_siwc_failures_use_fixed_stage_and_reason_without_external_output(self):
        cases = (
            ("[1/5] Password accepted TOTP 2FA challenge reached 2FA verification accepted SIWC_INTERACTION_REQUIRED", "mfa_accepted", "browser_interaction_required"),
            ("Password was rejected SIWC_INTERACTION_REQUIRED", "starting", "password_rejected"),
            ("2FA key was rejected SIWC_INTERACTION_REQUIRED", "starting", "totp_rejected"),
            ("SIWC_WORKSPACE_MISMATCH", "starting", "workspace_mismatch"),
            ("SIWC_AUTH_URL_INVALID", "starting", "invalid_auth_url"),
            ("PROXY_RISK_CONTROL", "starting", "security_check"),
            ("[proxy] 代理检测失败：HTTP 403，返回安全校验页面\n[error] 代理检测失败：HTTP 403，返回安全校验页面", "starting", "security_check"),
            ("ERR_MODULE_NOT_FOUND", "starting", "runtime_error"),
            ("ECONNREFUSED", "starting", "transport_error"),
            ("unknown upstream response", "starting", "protocol_error"),
        )
        for output, stage, reason in cases:
            with self.subTest(reason=reason):
                result = _siwc_password_failure(output + " synthetic-password synthetic-totp https://example.test/?code=secret", 1)
                self.assertIn(f"stage={stage}, reason={reason}, exit=1", result)
                self.assertIn("credentials retained", result)
                self.assertNotIn("synthetic", result)
                self.assertNotIn("secret", result)
                self.assertNotIn("https://", result)
                self.assertEqual("use browser authorization" in result, reason in {"browser_interaction_required", "security_check"})

    def test_parent_death_terminates_managed_protocol_descendants(self):
        with patch("os.getpgrp", return_value=123), patch("os.getpid", return_value=123), patch("os.killpg") as kill:
            with self.assertRaises(SystemExit):
                terminate_worker(15, None)
            kill.assert_called_once_with(123, 9)

    def test_siwc_runner_reports_browser_handoff_after_successful_mfa_without_exposing_output(self):
        api = SimpleNamespace(config=SimpleNamespace(tosub2_root=Path("synthetic-tosub2")), progress=lambda *_args: None)
        result = SimpleNamespace(returncode=1, stdout="Password accepted\n2FA verification accepted\nsynthetic-secret",
                                 stderr="SIWC_INTERACTION_REQUIRED https://example.test/?code=synthetic-code")
        with patch("tools.openai_oauth_reauth_worker.shutil.which", return_value="node"), patch.object(
            Path, "is_file", return_value=True
        ), patch("tools.openai_siwc_oauth_adapter.prepare_siwc_runtime", return_value=Path("synthetic-runner.mjs")), patch(
            "tools.openai_oauth_reauth_worker.subprocess.run", return_value=result
        ) as runner:
            with self.assertRaises(WorkerError) as failure:
                process_password_claim(api, dict(task_id=7, account_id=42, login_email="demo@example.test",
                                                password="synthetic-secret", oauth_profile="siwc",
                                                auth_url="https://auth.openai.com/api/accounts/authorize?state=synthetic"))
        self.assertIn("stage=mfa_accepted, reason=browser_interaction_required", str(failure.exception))
        self.assertIn("use browser authorization", str(failure.exception))
        self.assertNotIn("synthetic", str(failure.exception))
        self.assertEqual(runner.call_count, 1)
        self.assertEqual(runner.call_args.kwargs["timeout"], 600)

    def test_external_worker_does_not_signal_unrelated_process_group(self):
        with patch("os.getpgrp", return_value=100), patch("os.getpid", return_value=123), patch("os.killpg") as kill:
            with self.assertRaises(SystemExit):
                terminate_worker(15, None)
            kill.assert_not_called()

    def test_password_only_config_does_not_require_email_protocol(self):
        with tempfile.TemporaryDirectory() as root:
            source = Path(root) / "src"
            source.mkdir()
            (source / "protocol-login.mjs").touch()
            (source / "tls-transport.mjs").touch()
            env = {"OPENAI_REAUTH_WORKER_TOKEN": "x" * 32, "TOSUB2_ROOT": root}
            with patch.dict(os.environ, env, clear=True):
                config = WorkerConfig.from_env()
                self.assertIsNone(config.protocol_root)
                self.assertEqual(config.tosub2_root, Path(root))

    def test_rejects_missing_login_runtimes(self):
        with patch.dict(os.environ, {"OPENAI_REAUTH_WORKER_TOKEN": "x" * 32}, clear=True):
            with self.assertRaisesRegex(WorkerError, "configure TOSUB2_ROOT"):
                WorkerConfig.from_env()

    def test_explicit_invalid_email_protocol_is_not_silently_ignored(self):
        with tempfile.TemporaryDirectory() as root:
            source = Path(root) / "src"
            source.mkdir()
            (source / "protocol-login.mjs").touch()
            (source / "tls-transport.mjs").touch()
            env = {"OPENAI_REAUTH_WORKER_TOKEN": "x" * 32, "TOSUB2_ROOT": root, "CODEX_PROTOCOL_ROOT": root}
            with patch.dict(os.environ, env, clear=True):
                with self.assertRaisesRegex(WorkerError, "pure protocol and Sentinel assets"):
                    WorkerConfig.from_env()

    def test_email_claim_without_protocol_has_explicit_failure(self):
        with self.assertRaisesRegex(WorkerError, "email OTP re-login requires"):
            process_claim(SimpleNamespace(), None, {"credential_mode": "email_otp_url"})

    def test_check_does_not_load_email_protocol_or_claim_tasks(self):
        config = WorkerConfig("http://127.0.0.1:4040", "x" * 32, "check", None, Path("tosub2"))
        with patch("sys.argv", ["worker", "--check"]), patch.object(WorkerConfig, "from_env", return_value=config), patch(
            "tools.openai_oauth_reauth_worker.shutil.which", return_value="node"
        ), patch("tools.openai_oauth_reauth_worker.load_protocol") as protocol, patch(
            "tools.openai_oauth_reauth_worker.run_once"
        ) as run:
            self.assertEqual(main(), 0)
            protocol.assert_not_called()
            run.assert_not_called()

    def test_password_runner_does_not_inherit_worker_token_or_expose_output(self):
        api = SimpleNamespace(
            config=SimpleNamespace(tosub2_root=Path("synthetic-tosub2")),
            progress=lambda *_args: None,
        )
        result = SimpleNamespace(returncode=1, stdout="synthetic-secret", stderr="synthetic-secret")
        with patch.dict(os.environ, {"OPENAI_REAUTH_WORKER_TOKEN": "synthetic-worker-token"}), patch(
            "tools.openai_oauth_reauth_worker.shutil.which", return_value="node"
        ), patch.object(Path, "is_file", return_value=True), patch(
            "tools.openai_oauth_reauth_worker.subprocess.run", return_value=result
        ) as runner:
            with self.assertRaises(WorkerError) as raised:
                process_password_claim(api, {
                    "task_id": 7, "account_id": 42, "login_email": "demo@example.com",
                    "password": "synthetic-secret", "totp_secret": "synthetic-totp",
                })
        self.assertEqual(str(raised.exception), "password/TOTP protocol failed (exit 1)")
        self.assertNotIn("OPENAI_REAUTH_WORKER_TOKEN", runner.call_args.kwargs["env"])
        self.assertEqual(runner.call_args.kwargs["env"]["CHATGPT_LOGIN_PASSWORD"], "synthetic-secret")
        self.assertNotIn("synthetic-secret", runner.call_args.args[0])

    def test_workspace_id_from_auth_payload(self):
        self.assertEqual(
            _workspace_id_from_auth_payload(
                {"oai-client-auth-session": {"workspaces": [{"id": "org-test"}]}}
            ),
            "org-test",
        )
        invalid_payloads = (
            None,
            {},
            {"oai-client-auth-session": {}},
            {"oai-client-auth-session": {"workspaces": [{}]}},
        )
        for payload in invalid_payloads:
            with self.subTest(payload=payload):
                self.assertEqual(_workspace_id_from_auth_payload(payload), "")

    def test_bootstrap_reports_safe_endpoint_instead_of_circuit_cooldown(self):
        self.assertEqual(
            _safe_openai_endpoint("https://example.com/secret?state=secret"),
            "[redacted endpoint]",
        )

        class Response:
            status_code = 403
            url = "https://auth.openai.com/log-in?state=secret&code=secret"

        class Session:
            def get(self, *_args, **_kwargs):
                return Response()

        session = Session()

        def bootstrap(active_session, _state, auth_url=None):
            active_session.get(auth_url)

        with self.assertRaisesRegex(
            ProtocolHTTPError,
            r"OpenAI OAuth bootstrap returned HTTP 403 at auth\.openai\.com/log-in$",
        ):
            _checked_bootstrap_authorize(
                session,
                bootstrap,
                "state-1",
                auth_url="https://auth.openai.com/oauth/authorize?state=secret",
            )

    def test_process_claim_retries_bootstrap_403_with_a_fresh_session(self):
        events = []
        sessions = []

        class FakeSession:
            def __init__(self):
                self.number = len(sessions) + 1
                self.session = SimpleNamespace(close=lambda: events.append(("close", self.number)))
                sessions.append(self)

        class FakeProtocol:
            class AccountUnusableError(Exception):
                pass

            ProtocolSession = lambda **_kwargs: FakeSession()

            @staticmethod
            def network_preflight(session):
                events.append(("preflight", session.number))

            @staticmethod
            def bootstrap_authorize(session, _state, auth_url=None):
                events.append(("bootstrap", session.number))
                if session.number == 1:
                    raise ProtocolHTTPError("OpenAI OAuth bootstrap", 403, auth_url)

            submit_email = staticmethod(lambda session, _email: events.append(("email", session.number)))
            resend_email_otp = staticmethod(lambda _session: None)
            submit_email_otp = staticmethod(lambda _session, _code: None)
            select_workspace_and_get_callback = staticmethod(
                lambda _session, _state: "http://localhost:1455/auth/callback?code=callback-code&state=state-1"
            )
            extract_code = staticmethod(lambda _callback, _state: "callback-code")

        class FakeAPI:
            progress = staticmethod(lambda _task_id, _stage: None)
            callback = staticmethod(lambda _task_id, _callback: {"status": "succeeded"})

        claim = {
            "task_id": 7,
            "account_id": 42,
            "login_email": "mail@example.com",
            "otp_url": "http://127.0.0.1:12345/code",
            "auth_url": "https://auth.openai.com/authorize?state=state-1",
        }
        with (
            patch("tools.openai_oauth_reauth_worker.time.sleep"),
            patch("tools.openai_oauth_reauth_worker._wait_for_otp", return_value="482931"),
        ):
            process_claim(FakeAPI(), FakeProtocol, claim)

        self.assertEqual([event for event in events if event[0] == "preflight"], [("preflight", 1), ("preflight", 2)])
        self.assertEqual([event for event in events if event[0] == "bootstrap"], [("bootstrap", 1), ("bootstrap", 2)])
        self.assertLess(events.index(("preflight", 1)), events.index(("bootstrap", 1)))
        self.assertLess(events.index(("preflight", 2)), events.index(("bootstrap", 2)))
        self.assertEqual([event for event in events if event[0] == "email"], [("email", 2)])
        self.assertEqual([event for event in events if event[0] == "close"], [("close", 1), ("close", 2)])

    def test_auth_session_flow_exposes_only_safe_state(self):
        payload = {
            "email": "mail@example.com",
            "session_id": "secret-session",
            "email_verification_mode": "passwordless_login",
            "passwordless_disabled": False,
            "signup_mode": "email_login",
        }
        encoded = base64.urlsafe_b64encode(json.dumps(payload).encode()).decode().rstrip("=")
        cookie = SimpleNamespace(name="oai-client-auth-session", value=f"{encoded}.signature")
        session = SimpleNamespace(session=SimpleNamespace(cookies=SimpleNamespace(jar=[cookie])))

        self.assertEqual(
            _auth_session_flow(session),
            {
                "email_verification_mode": "passwordless_login",
                "passwordless_disabled": False,
                "signup_mode": "email_login",
            },
        )

    def test_email_submission_state_keeps_only_page_type_and_path(self):
        self.assertEqual(
            _email_submission_state(
                {
                    "continue_url": "https://auth.openai.com/log-in/password?secret=value",
                    "page": {"type": "login_password"},
                    "email": "mail@example.com",
                }
            ),
            ("login_password", "/log-in/password"),
        )

    def test_passwordless_intent_is_detected_without_reading_form_values(self):
        html = """
        <form method="post" action="/log-in/password">
          <input type="hidden" name="username" value="mail@example.com">
          <input type="password" name="current-password" value="must-not-submit">
          <button name="intent" value="passwordless_login_send_otp">Use code</button>
        </form>
        """
        self.assertTrue(_has_passwordless_intent(html))
        self.assertFalse(_has_passwordless_intent(html.replace("passwordless_login_send_otp", "password_login")))

    def test_passwordless_login_posts_empty_passwordless_request(self):
        calls = []

        class Response:
            def __init__(self, status_code, url, text=""):
                self.status_code = status_code
                self.url = url
                self.text = text

        class Session:
            def get_auth_navigate_headers(self, *, referer):
                return {"referer": referer}

            def get_auth_headers(self, *, referer):
                return {"referer": referer}

            def get(self, url, *, headers, allow_redirects):
                calls.append(("GET", url, dict(headers), allow_redirects))
                return Response(
                    200,
                    "https://auth.openai.com/log-in/password",
                    '<button name="intent" value="passwordless_login_send_otp">Use code</button>',
                )

            def post(self, url, *, headers, allow_redirects):
                calls.append(("POST", url, dict(headers), allow_redirects))
                response = Response(200, url)
                response.json = lambda: {"page": {"type": "email_otp_verification"}}
                return response

        path = _start_passwordless_login(Session(), "https://auth.openai.com/log-in/password")

        self.assertEqual(path, "/email-verification")
        self.assertEqual(
            [(call[0], call[1]) for call in calls],
            [
                ("GET", "https://auth.openai.com/log-in/password"),
                ("POST", "https://auth.openai.com/api/accounts/passwordless/send-otp"),
            ],
        )
        self.assertEqual(calls[1][2]["referer"], "https://auth.openai.com/log-in/password")
        self.assertEqual(calls[1][2]["content-type"], "application/json")
        self.assertFalse(calls[1][3])

    def test_process_claim_preflights_before_codex_protocol_flow(self):
        events = []

        class FakeSession:
            def __init__(self):
                self.session = SimpleNamespace(close=lambda: events.append("close"))

        class FakeProtocol:
            class AccountUnusableError(Exception):
                pass

            ProtocolSession = lambda **_kwargs: FakeSession()

            @staticmethod
            def network_preflight(_session):
                events.append("preflight")

            @staticmethod
            def bootstrap_authorize(_session, state, auth_url=None):
                events.append(("bootstrap", state, auth_url))

            @staticmethod
            def submit_email(_session, email):
                events.append(("email", email))

            @staticmethod
            def resend_email_otp(_session):
                events.append("resend")

            @staticmethod
            def submit_email_otp(_session, code):
                events.append(("otp", code))

            @staticmethod
            def select_workspace_and_get_callback(_session, state):
                events.append(("workspace", state))
                return "http://localhost:1455/auth/callback?code=callback-code&state=state-1"

            @staticmethod
            def extract_code(callback, state):
                events.append(("extract", callback, state))
                return "callback-code"

        class FakeAPI:
            def progress(self, task_id, stage):
                events.append(("progress", task_id, stage))

            def callback(self, task_id, callback):
                events.append(("callback", task_id, callback))
                return {"status": "succeeded"}

        claim = {
            "task_id": 7,
            "account_id": 42,
            "login_email": "mail@example.com",
            "otp_url": "http://127.0.0.1:12345/code",
            "auth_url": "https://auth.openai.com/authorize?state=state-1",
        }
        with patch("tools.openai_oauth_reauth_worker._wait_for_otp", return_value="482931"):
            process_claim(FakeAPI(), FakeProtocol, claim)

        names = [item[0] if isinstance(item, tuple) else item for item in events]
        self.assertLess(names.index("preflight"), names.index("bootstrap"))
        self.assertLess(names.index("bootstrap"), names.index("email"))
        self.assertLess(names.index("email"), names.index("otp"))
        self.assertLess(names.index("otp"), names.index("workspace"))
        self.assertLess(names.index("workspace"), names.index("callback"))
        self.assertNotIn("providers", names)
        self.assertNotIn("csrf", names)
        self.assertIn("close", names)

    def test_process_claim_uses_email_otp_resend_after_mailbox_timeout(self):
        events = []

        class FakeSession:
            def __init__(self):
                self.session = SimpleNamespace(close=lambda: None)

        class FakeProtocol:
            class AccountUnusableError(Exception):
                pass

            ProtocolSession = lambda **_kwargs: FakeSession()
            network_preflight = staticmethod(lambda _session: None)
            bootstrap_authorize = staticmethod(lambda _session, _state, auth_url=None: None)
            submit_email = staticmethod(lambda _session, _email: events.append("email"))
            resend_email_otp = staticmethod(lambda _session: events.append("resend"))
            submit_email_otp = staticmethod(lambda _session, _code: events.append("otp"))
            select_workspace_and_get_callback = staticmethod(
                lambda _session, _state: "http://localhost:1455/auth/callback?code=callback-code&state=state-1"
            )
            extract_code = staticmethod(lambda _callback, _state: "callback-code")

        class FakeAPI:
            progress = staticmethod(lambda _task_id, _stage: None)
            callback = staticmethod(lambda _task_id, _callback: {"status": "succeeded"})

        claim = {
            "task_id": 7,
            "account_id": 42,
            "login_email": "mail@example.com",
            "otp_url": "http://127.0.0.1:12345/code",
            "auth_url": "https://auth.openai.com/authorize?state=state-1",
        }
        with patch(
            "tools.openai_oauth_reauth_worker._wait_for_otp",
            side_effect=(WorkerError("mailbox timeout"), "482931"),
        ):
            process_claim(FakeAPI(), FakeProtocol, claim)

        self.assertEqual(events, ["email", "resend", "otp"])

    def test_process_claim_switches_login_password_to_email_otp(self):
        events = []

        class FakeSession:
            def __init__(self):
                self.session = SimpleNamespace(close=lambda: None, cookies=SimpleNamespace(jar=[]))

        class FakeProtocol:
            class AccountUnusableError(Exception):
                pass

            ProtocolSession = lambda **_kwargs: FakeSession()
            network_preflight = staticmethod(lambda _session: None)
            bootstrap_authorize = staticmethod(lambda _session, _state, auth_url=None: None)
            submit_email = staticmethod(
                lambda _session, _email: {
                    "continue_url": "https://auth.openai.com/log-in/password",
                    "page": {"type": "login_password"},
                }
            )
            start_passwordless_login = staticmethod(
                lambda _session, _url: events.append("passwordless")
            )
            resend_email_otp = staticmethod(lambda _session: events.append("resend"))
            submit_email_otp = staticmethod(lambda _session, _code: events.append("otp"))
            select_workspace_and_get_callback = staticmethod(
                lambda _session, _state: "http://localhost:1455/auth/callback?code=callback-code&state=state-1"
            )
            extract_code = staticmethod(lambda _callback, _state: "callback-code")

        class FakeAPI:
            progress = staticmethod(lambda _task_id, _stage: None)
            callback = staticmethod(lambda _task_id, _callback: {"status": "succeeded"})

        claim = {
            "task_id": 7,
            "account_id": 42,
            "login_email": "mail@example.com",
            "otp_url": "http://127.0.0.1:12345/code",
            "auth_url": "https://auth.openai.com/authorize?state=state-1",
        }
        with patch("tools.openai_oauth_reauth_worker._wait_for_otp", return_value="482931"):
            process_claim(FakeAPI(), FakeProtocol, claim)

        self.assertEqual(events, ["passwordless", "otp"])

    def test_process_claim_uses_tosub2_for_password_totp_without_logging_secrets(self):
        events = []
        with tempfile.TemporaryDirectory() as root_dir:
            root = Path(root_dir)
            (root / "src").mkdir()
            (root / "src" / "protocol-login.mjs").write_text("// test", encoding="utf-8")

            class FakeAPI:
                config = SimpleNamespace(tosub2_root=root)

                def progress(self, task_id, stage):
                    events.append(("progress", task_id, stage))

                def credentials(self, task_id, credentials, extra):
                    events.append(("credentials", task_id, credentials, extra))
                    return {"status": "succeeded"}

            def fake_run(command, **kwargs):
                self.assertEqual(Path(kwargs["cwd"]), Path(command[command.index("--sub2api-out") + 1]).parent)
                self.assertNotEqual(Path(kwargs["cwd"]), root)
                self.assertEqual(kwargs["env"]["CHATGPT_LOGIN_PASSWORD"], "password-secret")
                self.assertEqual(kwargs["env"]["CHATGPT_TOTP_SECRET"], "totp-secret")
                output_path = Path(command[command.index("--sub2api-out") + 1])
                output_path.write_text(
                    json.dumps(
                        {
                            "accounts": [
                                {
                                    "credentials": {
                                        "access_token": "access-secret",
                                        "refresh_token": "refresh-secret",
                                        "id_token": "id-secret",
                                    },
                                    "extra": {"client_id": "client-id"},
                                }
                            ]
                        }
                    ),
                    encoding="utf-8",
                )
                return SimpleNamespace(returncode=0, stdout="", stderr="")

            claim = {
                "task_id": 8,
                "account_id": 43,
                "credential_mode": "password_totp",
                "login_email": "mail@example.com",
                "password": "password-secret",
                "totp_secret": "totp-secret",
            }
            with patch("tools.openai_oauth_reauth_worker.subprocess.run", side_effect=fake_run):
                process_claim(FakeAPI(), None, claim)

        stages = [event[2] for event in events if event[0] == "progress"]
        self.assertEqual(stages, ["starting", "protocol_connecting", "applying_credentials"])
        credential_event = next(event for event in events if event[0] == "credentials")
        self.assertEqual(credential_event[2]["access_token"], "access-secret")
        self.assertEqual(credential_event[3]["client_id"], "client-id")

    def test_extracts_structured_and_html_otp(self):
        self.assertEqual(extract_otp('{"verification_code":"123456"}'), "123456")
        self.assertEqual(extract_otp("<p>Your ChatGPT verification code is <b>654321</b></p>"), "654321")

    def test_rejects_stale_structured_otp(self):
        old = time.time() - 60
        body = json.dumps({"code": "123456", "received_at": old})
        self.assertEqual(extract_otp(body, after_ts=time.time()), "")

    def test_fetches_otp_without_worker_credentials(self):
        server = ThreadingHTTPServer(("127.0.0.1", 0), OTPHandler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            provider = OTPURLProvider(f"http://127.0.0.1:{server.server_port}/code")
            self.assertEqual(provider("mail@example.com"), "482931")
        finally:
            server.shutdown()
            server.server_close()

    def test_revalidates_mailbox_address_before_every_request(self):
        server = ThreadingHTTPServer(("127.0.0.1", 0), OTPHandler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        url = f"http://127.0.0.1:{server.server_port}/code"
        try:
            with patch(
                "tools.openai_oauth_reauth_worker.validate_otp_url",
                wraps=validate_otp_url,
            ) as validate:
                provider = OTPURLProvider(url)
                self.assertEqual(provider("mail@example.com"), "482931")
                self.assertEqual(validate.call_count, 2)
        finally:
            server.shutdown()
            server.server_close()

    def test_rejects_short_worker_token(self):
        previous = dict(os.environ)
        try:
            os.environ["SUB2API_BASE_URL"] = "http://127.0.0.1:8080"
            os.environ["OPENAI_REAUTH_WORKER_TOKEN"] = "short"
            os.environ["CODEX_PROTOCOL_ROOT"] = os.getcwd()
            with self.assertRaises(WorkerError):
                WorkerConfig.from_env()
        finally:
            os.environ.clear()
            os.environ.update(previous)

    def test_rejects_private_https_and_non_loopback_http_mailboxes(self):
        with patch.dict(os.environ, {"OPENAI_REAUTH_TRUSTED_OTP_HOSTS": ""}):
            with self.assertRaises(WorkerError):
                validate_otp_url("https://127.0.0.1/code")
            with self.assertRaises(WorkerError):
                validate_otp_url("http://192.168.1.10/code")

    def test_allows_private_https_only_for_explicitly_trusted_mailbox_host(self):
        private_address = [(None, None, None, None, ("192.168.1.10", 443))]
        with patch.dict(os.environ, {"OPENAI_REAUTH_TRUSTED_OTP_HOSTS": "mail.internal"}), patch(
            "tools.openai_oauth_reauth_worker.socket.getaddrinfo",
            return_value=private_address,
        ):
            validate_otp_url("https://mail.internal/code")
            with self.assertRaises(WorkerError):
                validate_otp_url("https://other.internal/code")
            with self.assertRaises(WorkerError):
                validate_otp_url("http://mail.internal/code")

    def test_rejects_plain_http_remote_control_api(self):
        previous = dict(os.environ)
        try:
            os.environ["SUB2API_BASE_URL"] = "http://example.com"
            os.environ["OPENAI_REAUTH_WORKER_TOKEN"] = "x" * 32
            os.environ["CODEX_PROTOCOL_ROOT"] = os.getcwd()
            with self.assertRaises(WorkerError):
                WorkerConfig.from_env()
        finally:
            os.environ.clear()
            os.environ.update(previous)

    def test_sanitizes_urls_oauth_params_and_otps(self):
        raw = (
            'failed https://mail.test/code?key=secret&state=abc for user@example.com '
            'with OTP 123456 and {"refresh_token":"hidden"} Bearer eyJhbGciOiJub25l.eyJzdWIiOiIxIn0.sig'
        )
        safe = sanitize_error(raw)
        self.assertNotIn("secret", safe)
        self.assertNotIn("abc", safe)
        self.assertNotIn("123456", safe)
        self.assertNotIn("https://", safe)
        self.assertNotIn("user@example.com", safe)
        self.assertNotIn("hidden", safe)
        self.assertNotIn("eyJ", safe)

    def test_sanitizes_provider_html_errors(self):
        safe = sanitize_error(
            "chatgpt-login status=403, body=<html><style>body{color:red}</style><body>Access denied</body></html>"
        )
        self.assertEqual(safe, "chatgpt-login status=403, body= Access denied")

    def test_wait_for_otp_skips_a_code_that_already_failed(self):
        codes = iter(("123456", "654321"))

        def provider(_email, after_ts=None):
            return next(codes)

        self.assertEqual(
            _wait_for_otp(
                provider,
                "mail@example.com",
                time.time(),
                timeout=1,
                interval=0.001,
                excluded_codes={"123456"},
            ),
            "654321",
        )

    def test_codex_callback_requires_exact_redirect_uri(self):
        self.assertTrue(
            _is_local_codex_callback(
                "http://localhost:1455/auth/callback?code=test-code&state=test-state"
            )
        )
        for callback in (
            "https://localhost:1455/auth/callback?code=test&state=test",
            "http://127.0.0.1:1455/auth/callback?code=test&state=test",
            "http://[::1]:1455/auth/callback?code=test&state=test",
            "http://localhost:1456/auth/callback?code=test&state=test",
            "http://localhost:1455/other?code=test&state=test",
            "http://localhost:1455/auth/callback/extra?code=test&state=test",
            "http://user@localhost:1455/auth/callback?code=test&state=test",
            "http://localhost:1455/auth/callback?code=test&state=test#fragment",
        ):
            with self.subTest(callback=callback):
                self.assertFalse(_is_local_codex_callback(callback))


if __name__ == "__main__":
    unittest.main()

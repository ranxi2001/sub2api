"""Real WorkerAPI HTTP + rotate + sealed journal + actual patched CLI; upstream/API storage mocked."""
import importlib
import json
import os
from pathlib import Path
import shutil
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from unittest.mock import patch

TOOLS = Path(__file__).resolve().parent
sys.path.insert(0, os.environ.get('TOTP_TEST_WORKER_ROOT', str(TOOLS)))
worker = importlib.import_module('openai_oauth_reauth_worker')
rotation = importlib.import_module('openai_totp_rotation')
OLD='JBSWY3DPEHPK3PXP'
NEW='GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ'

class WorkerIntegrationTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root=Path(self.temp.name)
        src=self.root/'protocol/src';src.mkdir(parents=True)
        shutil.copyfile(os.environ['TOTP_TEST_PROTOCOL'],src/'protocol-login.mjs')
        shutil.copyfile(TOOLS/'fixtures/totp-http-transport.mjs',src/'tls-transport.mjs')
        self.src=src
        self.trace=self.root/'http.jsonl'
        self.fixture={'factors':['F1','F2'],'codes':{'F1':rotation.totp(OLD,59),'F2':rotation.totp(NEW,59)},'trace':str(self.trace)}
        self.claim=dict(task_id=7,account_id=42,login_email='owner@example.test',password='synthetic',totp_secret=OLD)
        self.requests=[];self.finish=None;self.errors=[]
        outer=self
        class API(BaseHTTPRequestHandler):
            def log_message(self,*args): pass
            def do_POST(self):
                data=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                status=200
                try:
                    assert self.headers['X-OpenAI-Reauth-Worker-Token']=='synthetic-worker'
                    outer.requests.append((self.path,data))
                    if self.path.endswith('/totp-phase') and data['phase']=='prepared':
                        saved=outer.journal.call({'action':'list'})[0]
                        assert saved['candidate']==data['secret']==NEW
                        assert saved['factor_id']==data['factor_id']=='F2'
                        assert saved['session_id']==data['enrollment_session_id']=='enrollment'
                    if self.path.endswith('/totp-phase') and data['phase']=='bound':
                        assert data['secret']==NEW and data['factor_id']=='F2'
                    if self.path.endswith('/totp-finish'): outer.finish=data
                except Exception as exc:
                    outer.errors.append(type(exc).__name__);status=400
                self.send_response(status);self.send_header('Content-Type','application/json');self.end_headers()
                self.wfile.write(json.dumps({'code':0 if status==200 else 1,'data':{}}).encode())
        self.server=HTTPServer(('127.0.0.1',0),API)
        thread=threading.Thread(target=self.server.serve_forever,daemon=True);thread.start()
        def close(): self.server.shutdown();self.server.server_close();thread.join()
        self.addCleanup(close)
        env=patch.dict(os.environ,{'OPENAI_TOTP_JOURNAL_DIR':str(self.root/'journal')})
        env.start();self.addCleanup(env.stop)
        self.journal=rotation.Journal()
        self.api=worker.WorkerAPI(worker.WorkerConfig(base_url=f'http://127.0.0.1:{self.server.server_port}',worker_token='synthetic-worker',worker_id='writer',protocol_root=None,tosub2_root=src.parent))

    def run_rotation(self, timeout=False):
        (self.src/'totp-fixture.json').write_text(json.dumps(self.fixture))
        outer=self
        class MFA:
            def request(self,method,route,payload):
                if route.endswith('/enroll'): return {'secret':NEW,'session_id':'enrollment','factor_id':'F2'}
                assert route.endswith('/activate_enrollment')
                saved=outer.journal.call({'action':'list'})[0]
                assert saved['database_acknowledged']
                if timeout: raise TimeoutError('ambiguous activation')
                return {'success':True}
            def close(self): pass
        with patch.object(rotation.time, 'sleep'):
            rotation.rotate(self.api,self.claim,session_factory=lambda *_:MFA())

    def test_actual_worker_http_and_patched_cli_promote_only_verified_pair(self):
        self.run_rotation()
        self.assertEqual(self.errors,[])
        self.assertTrue(self.finish['success'])
        phases=[body['phase'] for route,body in self.requests if route.endswith('/totp-phase')]
        self.assertEqual(phases,['enrolling','prepared','activating','verifying','bound'])
        requests=[json.loads(line) for line in self.trace.read_text().splitlines()]
        self.assertEqual([r['id'] for r in requests if '/mfa/' in r['route']],['F2','F2'])
        saved=rotation.Journal().call({'action':'list'})[0]
        self.assertEqual(saved['factor_id'],'F2')
        for file in (self.root/'journal').iterdir():
            self.assertNotIn(NEW.encode(),file.read_bytes())
            self.assertNotIn(b'enrollment',file.read_bytes())

    def test_activation_uncertainty_preserves_pair_and_recovery_does_not_enroll(self):
        self.run_rotation(timeout=True)
        self.assertEqual(self.errors,[])
        self.assertFalse(self.finish['success'])
        saved=rotation.Journal().call({'action':'list'})[0]
        self.assertEqual((saved['candidate'],saved['factor_id']),(NEW,'F2'))
        self.requests.clear()
        self.claim.update(action='verify_new',candidate_secret=NEW,candidate_factor_id='F2',candidate_enrollment_session_id='enrollment')
        self.run_rotation()
        self.assertTrue(self.finish['success'])
        self.assertEqual([body['phase'] for route,body in self.requests if route.endswith('/totp-phase')],['verify_recovery','bound'])

    def test_legacy_unbound_candidate_matches_in_both_factor_orders(self):
        for ids in [['F1','F2'],['F2','F1']]:
            with self.subTest(order=ids):
                self.fixture['factors']=ids
                (self.src/'totp-fixture.json').write_text(json.dumps(self.fixture))
                data=rotation.login_web(self.api.config,self.claim,NEW)
                self.assertEqual(data['web']['mfaFactorId'],'F2')

    def test_raw_cli_explicit_nonotp_code_stops_before_second_factor(self):
        self.fixture.update(failure=400,error={'error':{'code':'session_expired','message':'incorrect code'}})
        (self.src/'totp-fixture.json').write_text(json.dumps(self.fixture))
        with self.assertRaises(rotation.RotationError):
            rotation.login_web(self.api.config,self.claim,NEW)
        requests=[json.loads(line) for line in self.trace.read_text().splitlines()]
        self.assertEqual([r['id'] for r in requests if '/mfa/' in r['route']],['F1','F1'])

if __name__=='__main__': unittest.main()

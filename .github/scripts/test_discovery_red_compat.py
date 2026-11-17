"""Lightweight source-only checks; never compile or run application code."""
import importlib.util
import subprocess
import tempfile
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("compat", Path(__file__).with_name("prepare-discovery-red-compat.py"))
COMPAT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(COMPAT)
ROOT = Path(__file__).resolve().parents[2]
PENDING = "c213c632e428423ecc271ace88743f7b1d64b48e"
STORAGE = "dc3a5403be7053397c43c2ef4b1e4fff01536433"
FIXTURE = COMPAT.SERVICE + "openai_agent_pending_observation_test.go"


class DiscoveryCompatibilityTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.tree = Path(self.temp.name) / "checkout"
        subprocess.run(["git", "clone", "--shared", "--no-checkout", str(ROOT), str(self.tree)], check=True, capture_output=True)
        self.candidate = COMPAT.git(self.tree, "rev-parse", "HEAD").decode().strip()
        for path in (COMPAT.PROVENANCE, COMPAT.DELIVERY, COMPAT.MANAGER, FIXTURE):
            target = self.tree / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(COMPAT.git(self.tree, "show", self.candidate + ":" + path))

    def restore(self, baseline, path):
        data = COMPAT.git(self.tree, "show", baseline + ":" + path)
        (self.tree / path).write_bytes(data)
        return data

    def test_both_pending_oracles_keep_every_historical_byte(self):
        for baseline in (PENDING, STORAGE):
            with self.subTest(baseline=baseline):
                original = self.restore(baseline, COMPAT.DELIVERY)
                manifest = COMPAT.prepare(baseline, self.tree, self.candidate, [COMPAT.DELIVERY], [FIXTURE])
                actual = (self.tree / COMPAT.DELIVERY).read_bytes()
                field = COMPAT.unique(actual, rb"^\tdiscoveryBindings[ \t]+map\[string\]openAIToolProofSnapshot\n")
                self.assertEqual(original, actual.replace(field, b"", 1))
                self.assertEqual(COMPAT.blob(actual), manifest["compatibility"][0]["overlay_blob"])
                self.assertIn(FIXTURE, manifest["selected_candidate_oracles"])

    def test_ws_layout_only_and_scanner_closure(self):
        for baseline in ("58628d98b9e0781c1b8e3df6259ab487a1490f70", "b9feadf7dd6228a4117e360821d9994b7cc54897",
                         "04ec5e737ecb925faa2e8f3b00efff816715c55c", "72c21bedea567d8032f736adba16edcf5645ca19",
                         "2927209f5492d8cb690b83a1964b2f093c19ad7a", "4b13c785786ff1d623bc23203991dac405494567"):
            with self.subTest(baseline=baseline):
                original = self.restore(baseline, COMPAT.MANAGER)
                result = COMPAT.prepare(baseline, self.tree, self.candidate, [COMPAT.MANAGER])
                actual = (self.tree / COMPAT.MANAGER).read_bytes()
                self.assertTrue(actual.startswith(original), "historical methods must stay byte-identical")
                self.assertIn(b"type openAIWSDiscoveryHandoff struct", actual)
                self.assertIn(b"func openAIWSHasOpaqueField(", actual)
                self.assertTrue(result["compatibility"][0]["historical_bytes_preserved"])

    def test_prior_pinned_wire_layout_is_preserved(self):
        original = self.restore(COMPAT.LEGACY_ANCHOR_BASELINE, COMPAT.MANAGER)
        anchor = b"type openAIWSAgentRecovery struct {\n"
        preimage = original.replace(anchor, anchor + b"\tcompletedBridge openAIWSBridgeResponseAnchor\n", 1)
        self.assertEqual(COMPAT.LEGACY_ANCHOR_BLOB, COMPAT.blob(preimage))
        (self.tree / COMPAT.MANAGER).write_bytes(preimage)
        COMPAT.prepare(COMPAT.LEGACY_ANCHOR_BASELINE, self.tree, self.candidate, [COMPAT.MANAGER])
        self.assertTrue((self.tree / COMPAT.MANAGER).read_bytes().startswith(preimage))

    def test_dirty_source_caller_or_oracle_is_rejected_before_write(self):
        for target in (COMPAT.DELIVERY, COMPAT.PROVENANCE, FIXTURE):
            with self.subTest(target=target):
                original = self.restore(PENDING, COMPAT.DELIVERY)
                clean = (self.tree / target).read_bytes()
                (self.tree / target).write_bytes(clean + b"\n// unexpected edit\n")
                before = (self.tree / COMPAT.DELIVERY).read_bytes()
                with self.assertRaises(ValueError):
                    COMPAT.prepare(PENDING, self.tree, self.candidate, [COMPAT.DELIVERY], [FIXTURE])
                self.assertEqual(before, (self.tree / COMPAT.DELIVERY).read_bytes())
                (self.tree / target).write_bytes(clean)

    def test_candidate_green_is_byte_identical(self):
        before = (self.tree / COMPAT.DELIVERY).read_bytes()
        result = COMPAT.prepare(PENDING, self.tree, self.candidate, [COMPAT.DELIVERY], [FIXTURE])
        self.assertEqual(before, (self.tree / COMPAT.DELIVERY).read_bytes())
        self.assertEqual("unchanged candidate", result["compatibility"][0]["status"])


if __name__ == "__main__":
    unittest.main()

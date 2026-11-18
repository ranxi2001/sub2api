"""Source-only checks for storage compatibility in historical comparison trees."""
import importlib.util
import subprocess
import tempfile
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location('compat', Path(__file__).with_name('prepare-openai-usage-red-compat.py'))
COMPAT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(COMPAT)
ROOT = Path(__file__).resolve().parents[2]


class UsageCompatibilityTest(unittest.TestCase):
    def test_original_executable_bytes_and_fields_are_preserved(self):
        for baseline in ('f895ea743f34a4c4ab6c9d01c01e13c931625a6e', '6df0cd5bb0ce4d774d34004008f9dd661d236468'):
            with self.subTest(baseline=baseline), tempfile.TemporaryDirectory() as temp:
                tree = Path(temp) / 'tree'
                subprocess.run(['git', 'init', '-q', str(tree)], check=True, capture_output=True)
                # A shallow clone can omit detached historical fetches. Share
                # the already-verified object store directly, without copying
                # refs or authentication settings from the source checkout.
                objects = subprocess.check_output(['git', '-C', str(ROOT), 'rev-parse', '--git-path', 'objects'], text=True).strip()
                objects = (ROOT / objects).resolve()
                (tree / '.git/objects/info/alternates').write_text(str(objects) + '\n')
                original = subprocess.check_output(['git', '-C', str(tree), 'show', baseline + ':' + COMPAT.TARGET])
                target = tree / COMPAT.TARGET
                target.parent.mkdir(parents=True)
                target.write_bytes(original)
                result = COMPAT.prepare(tree, baseline)
                self.assertEqual(original, target.read_bytes().replace(result['added_storage'].encode(), b'', 1))
                self.assertTrue(result['historical_bytes_preserved'])
                for name, _ in COMPAT.FIELDS:
                    self.assertEqual(1, target.read_text().count(name))
                # A second application or a dirty source must not be accepted.
                with self.assertRaises(ValueError):
                    COMPAT.prepare(tree, baseline)
                target.write_bytes(original + b'\n// unexpected edit\n')
                with self.assertRaises(ValueError):
                    COMPAT.prepare(tree, baseline)
                self.assertEqual(original + b'\n// unexpected edit\n', target.read_bytes())


if __name__ == '__main__':
    unittest.main()

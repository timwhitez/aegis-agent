import tempfile
import unittest
from pathlib import Path

from task_plan import read_plan


class TaskPlanTest(unittest.TestCase):
    def test_section_and_key_order_have_no_effect(self):
        agent = '[environment]\ndocker_image="solver"\ncpus=2\nmemory_mb=2048'
        verifier = ('[verifier]\n# independent deadline\nenvironment_mode="separate"\n'
                    'timeout_sec=60\n[verifier.environment]\ndocker_image="judge"\n'
                    'memory_mb=512\ncpus=1')
        with tempfile.TemporaryDirectory() as temp:
            task = Path(temp)
            for sections in ((agent, verifier), (verifier, agent)):
                (task / 'task.toml').write_text('[agent]\ntimeout_sec=25\n' + '\n'.join(sections))
                plan = read_plan(task)
                self.assertEqual(plan['agent'], dict(image='solver', cpus=2, memory_mb=2048, timeout_sec=25))
                self.assertEqual(plan['verifier'], dict(image='judge', cpus=1, memory_mb=512, timeout_sec=60))
                self.assertEqual(plan['mode'], 'separate')

    def test_missing_image_rejected_before_execution(self):
        with tempfile.TemporaryDirectory() as temp:
            task = Path(temp)
            (task / 'task.toml').write_text('[agent]\ntimeout_sec=15\n[environment]\ncpus=1\nmemory_mb=512')
            with self.assertRaisesRegex(ValueError, 'prebuilt'):
                read_plan(task)

    def test_operator_paths_require_a_shared_task_without_native_artifacts(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            spec = '[agent]\ntimeout_sec=7\n[environment]\ndocker_image="a"\ncpus=1\nmemory_mb=512\n'
            (base/'task.toml').write_text(spec)
            self.assertEqual(read_plan(base, ['/app/out'])['capture_paths'], ['/app/out'])
            for paths in (['/app'], ['/etc/shadow'], ['/sys/fs/x'], ['/app/../secrets'],
                          ['/app/.env'], ['/app/*'], ['/app/out', '/app/out/file']):
                with self.subTest(paths=paths), self.assertRaises(ValueError):
                    read_plan(base, paths)
            for extra in ('artifacts=["/app/result"]\n'+spec,
                          spec+'[verifier.environment]\ndocker_image="v"\ncpus=1\nmemory_mb=512\n'):
                (base/'task.toml').write_text(extra)
                with self.assertRaisesRegex(ValueError, 'without declared artifacts'):
                    read_plan(base, ['/app/out'])


if __name__ == '__main__':
    unittest.main()

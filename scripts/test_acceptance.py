import importlib.util
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location('acceptance_macos', Path(__file__).with_name('acceptance-macos.py'))
acceptance = importlib.util.module_from_spec(spec)
spec.loader.exec_module(acceptance)


def message(local_id):
    return {'id': {'local_id': local_id, 'server_id_str': str(local_id), 'talker': 'fixture'}}


class DigestAcceptance(unittest.TestCase):
    def test_empty_incremental_window_is_valid(self):
        for empty in [[], None]:
            self.assertEqual(1, acceptance.validate_digest_windows({'messages': [message(1)]}, {'messages': empty}))

    def test_first_window_must_contain_actual_messages(self):
        for empty in [[], None]:
            with self.assertRaises(ValueError):
                acceptance.validate_digest_windows({'messages': empty}, {'messages': [message(2)]})

    def test_repeated_messages_still_fail(self):
        for first, second in [([message(1)], [message(1)]), ([message(1), message(1)], [])]:
            with self.assertRaises(ValueError):
                acceptance.validate_digest_windows({'messages': first}, {'messages': second})

    def test_missing_or_malformed_results_are_not_empty_windows(self):
        for bad in [{}, {'messages': {}}, {'messages': [{}]}]:
            with self.assertRaises(ValueError):
                acceptance.validate_digest_windows({'messages': [message(1)]}, bad)


if __name__ == '__main__':
    unittest.main()

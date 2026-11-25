import importlib.util
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location('acceptance_macos', Path(__file__).with_name('acceptance-macos.py'))
acceptance = importlib.util.module_from_spec(spec)
spec.loader.exec_module(acceptance)


def message(local_id):
    return {'id': {'local_id': local_id, 'server_id_str': str(local_id), 'talker': 'fixture'}}


class SessionAcceptance(unittest.TestCase):
    def test_timeline_uses_the_conversation_username(self):
        for kind in ('official_account', 'private', 'group'):
            with self.subTest(kind=kind):
                rows = [
                    {'chat_type': 'folded', 'username': 'fixture_folder'},
                    {'chat_type': kind, 'username': 'fixture_chat'},
                ]
                messages = [message(1)]

                def call(tool, args):
                    if tool == 'sessions':
                        allowed = args['type_filter'].split(',')
                        return {'sessions': [row for row in rows if row['chat_type'] in allowed]}
                    self.assertEqual(tool, 'chat_timeline')
                    self.assertEqual(args['talker'], 'fixture_chat')
                    return {'messages': messages}

                self.assertEqual(acceptance.sample_timeline(call), (1, 'fixture_chat', messages))


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

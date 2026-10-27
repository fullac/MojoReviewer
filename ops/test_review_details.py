import base64
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import Mock

sys.path.insert(0, str(Path(__file__).parent))
from start import Handler, Manager, atomic_json
from review_details import ReviewDetails, DetailError, PREVIEW, read_inside


class ReviewDetailsTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.data = self.root / 'data'
        atomic_json(self.root / '.MojoReviewer/config.json', {
            'data_dir': str(self.data), 'github': {'repos': ['acme/web'], 'webhook_secret': 'private-value'},
            'agent': {'api_key': 'private-model'}})
        self.manager = Manager(self.root)
        self.row = {'id': 'github:delivery-1', 'repo': 'acme/web', 'number': 7,
                    'head_sha': 'unique-head', 'updated_at': '2026-01-02T00:00:00Z',
                    'state': 'completed', 'comment_url': 'https://github.com/acme/web/pull/7#issuecomment-123'}
        self.manager.record_data = lambda: ({}, [self.row])
        self.manager.gh = Mock(return_value={'id': 123, 'html_url': self.row['comment_url'],
                                            'body': '# Result\n\nHost-rendered private-value', 'user': {'login': 'bot'}})
        self.sessions = self.data / 'luckyagent/sessions'
        self.messages = [{'role': 'user', 'content': 'task unique-head', 'created_at': '2026-01-01T01:00:00Z'},
                         {'role': 'assistant', 'content': 'done', 'tool_calls': [{'id': 'call-1', 'name': 'pr_diff_stat', 'arguments': '{}'}]},
                         {'role': 'tool', 'tool_call_id': 'call-1', 'content': 'private-model ' + str(Path.home()) + '/fixture'}]
        self.session('s1', self.messages)

    def session(self, sid, messages, title='acme/web#7'):
        atomic_json(self.sessions / (sid + '.meta.json'), {'id': sid, 'title': title,
                    'created_at': '2026-01-01T00:00:00Z', 'message_count': len(messages), 'format': 'segment_v1'})
        directory = self.sessions / sid
        directory.mkdir(exist_ok=True)
        (directory / 'messages.jsonl').write_text(''.join(json.dumps(m) + '\n' for m in messages))

    def detail(self):
        return ReviewDetails(self.manager, self.row['id'])

    def expect_error(self, status, callback):
        with self.assertRaises(DetailError) as caught:
            callback()
        self.assertEqual(caught.exception.status, status)

    def test_exact_published_comment_not_assistant_text(self):
        content = self.detail().content()
        self.assertIn('Host-rendered [redacted]', content['body'])
        self.manager.gh.assert_called_once_with('repos/acme/web/issues/comments/123')
        self.assertEqual(content['source'], 'github')
        self.row.pop('comment_url')
        self.assertFalse(self.detail().content()['available'])

    def test_comment_scope_response_identity_and_safe_errors(self):
        original = self.row['comment_url']
        for url in ['https://evil.test/acme/web/pull/7#issuecomment-123',
                    'https://github.com/acme/other/pull/7#issuecomment-123',
                    'https://github.com/acme/web/pull/8#issuecomment-123',
                    'https://user@github.com/acme/web/pull/7#issuecomment-123',
                    'https://github.com/acme/web/pull/7?x=1#issuecomment-123', 'javascript:alert(1)']:
            self.row['comment_url'] = url
            self.expect_error(422, self.detail().content)
        self.manager.gh.assert_not_called()
        self.row['comment_url'] = original
        self.manager.gh.return_value['id'] = 124
        self.expect_error(502, self.detail().content)
        self.manager.gh.side_effect = RuntimeError('private-value')
        with self.assertRaises(DetailError) as caught:
            self.detail().content()
        self.assertNotIn('private-value', str(caught.exception))
        self.row['repo'] = 'other/repo'
        self.expect_error(403, self.detail)

    def test_enterprise_host(self):
        self.manager.cfg['github']['host'] = 'git.example.test'
        self.row['comment_url'] = self.row['comment_url'].replace('github.com', 'git.example.test')
        self.manager.gh.return_value['html_url'] = self.row['comment_url']
        self.assertTrue(self.detail().content()['available'])

    def test_old_inference_and_exact_session_binding(self):
        data = self.detail().transcript()
        self.assertEqual((data['association'], data['session_id'], data['total']), ('inferred','s1',3))
        self.row['session_id'] = 's1'
        self.session('s2', self.messages)
        data = self.detail().transcript()
        self.assertEqual(data['association'], 'exact')
        self.assertEqual([c['id'] for c in data['candidates']], ['s1'])
        self.expect_error(403, lambda: self.detail().transcript('s2'))
        self.row['session_id'] = '../../outside'
        self.expect_error(403, self.detail().transcript)

    def test_ambiguous_legacy_requires_choice_and_rejects_other_pr(self):
        self.session('s2', self.messages)
        self.session('other', self.messages, title='acme/web#8')
        data = self.detail().transcript()
        self.assertEqual(data['association'], 'ambiguous')
        self.assertFalse(data['session_id'])
        self.assertEqual(self.detail().transcript('s2')['session_id'], 's2')
        self.expect_error(403, lambda: self.detail().transcript('other'))
        self.session('s2', [{'role':'user','content':'different-head','created_at':'2026-01-01T02:00:00Z'}])
        self.assertEqual(self.detail().transcript()['session_id'], 's1')

    def test_pagination_redaction_and_version_conflict(self):
        data = self.detail().transcript(limit=2)
        self.assertEqual(data['next_offset'], 2)
        self.assertTrue(data['has_more'])
        next_page = self.detail().transcript(offset=2, version=data['version'])
        raw = json.dumps(next_page)
        self.assertNotIn('private-model', raw)
        self.assertNotIn(str(Path.home()), raw)
        self.assertIn('tool_call_id', raw)
        self.assertIn('pr_diff_stat', json.dumps(data))
        self.session('s1', self.messages + [{'role':'assistant','content':'new'}])
        self.expect_error(409, lambda: self.detail().transcript(offset=2, version=data['version']))
        self.expect_error(400, lambda: self.detail().transcript(limit=101))

    def test_full_blob_and_unicode_chunks_are_not_truncated(self):
        text = '中文🥕' * 40000 + ' END'
        raw = text.encode()
        digest = hashlib.sha256(raw).hexdigest()
        self.session('s1',[{'role':'tool','content':'preview','blob_hash':digest}])
        blobs = self.sessions / 's1/blobs'
        blobs.mkdir()
        (blobs / digest).write_bytes(raw)
        data = self.detail().transcript()
        block = data['messages'][0]['blocks'][0]
        self.assertTrue(block['has_more'])
        result = block['text']
        while len(result) < block['total']:
            chunk = self.detail().message_block('s1','live',data['version'],0,'content',len(result))
            result += chunk['text']
        self.assertEqual(result, text)
        (blobs / digest).write_text('corrupt')
        self.assertIn('error', self.detail().transcript()['messages'][0])
        self.expect_error(422, lambda: self.detail().message_block('s1','live',data['version'],0,'content',PREVIEW))

    def test_backups_with_embedded_blobs_and_compaction(self):
        self.session('s1',[{'role':'system','name':'compact_boundary','content':'summary'}])
        data = self.detail().transcript()
        self.assertTrue(data['warnings'])
        body = b'original full body'
        digest = hashlib.sha256(body).hexdigest()
        atomic_json(self.sessions / '.backups/s1/backup-123.json', {'session_id':'s1','title':'acme/web#7',
                    'messages':[{'role':'tool','blob_hash':digest,'content':'preview'}],
                    'blobs':{digest:base64.b64encode(body).decode()}})
        data = self.detail().transcript(source='backup-123')
        self.assertEqual(data['messages'][0]['blocks'][0]['text'], body.decode())
        self.expect_error(404, lambda: self.detail().transcript(source='../private'))

    def test_legacy_markdown_and_incomplete_jsonl(self):
        meta = self.sessions / 's1.meta.json'
        value = json.loads(meta.read_text()); value['format'] = 'markdown'; atomic_json(meta, value)
        (self.sessions / 's1/messages.jsonl').unlink()
        (self.sessions / 's1.md').write_text('# Session\n```json\n' + json.dumps({**value,'messages':self.messages}) + '\n```\n')
        self.assertEqual(self.detail().transcript()['total'], 3)
        self.session('s1', self.messages)
        path = self.sessions / 's1/messages.jsonl'
        with path.open('a') as f: f.write('{"role":')
        data = self.detail().transcript()
        self.assertEqual(data['total'],3)
        self.assertTrue(data['warnings'])
        with path.open('a') as f: f.write('\n')
        self.expect_error(422, self.detail().transcript)

    def test_paths_and_context_identity_are_checked(self):
        outside = self.root / 'outside.json'; outside.write_text('{}')
        path = self.sessions / 's1/messages.jsonl'; path.unlink(); path.symlink_to(outside)
        self.expect_error(403, self.detail().transcript)
        self.expect_error(403, lambda: read_inside(self.sessions, '../../outside.json'))
        path.unlink(); self.session('s1', self.messages)
        key = hashlib.sha256(self.row['id'].encode()).hexdigest()
        context = {'attempt_id':self.row['id'],'repo':'acme/web','number':7,'session_id':'s1',
                   'system_prompt':'private-value','task_prompt':'actual task','message_start':4}
        atomic_json(self.data / 'review_context' / (key+'.json'), context)
        self.assertEqual(self.detail().transcript()['context']['system_prompt'], '[redacted]')
        context['session_id'] = 's2'; atomic_json(self.data / 'review_context' / (key+'.json'), context)
        self.expect_error(403, self.detail().transcript)

    def test_http_routes_validate_inputs_and_remain_local_read_only(self):
        handler = object.__new__(Handler)
        handler.server = types.SimpleNamespace(server_port=9970, manager=self.manager)
        handler.respond = Mock()
        handler.headers = {'Host':'127.0.0.1:9970'}
        for path, status in [('/api/reviews/session?id=github%3Adelivery-1',200),
                             ('/api/reviews/content?id=github%3Adelivery-1',200),
                             ('/api/reviews/session?id=github%3Adelivery-1&offset=x',400),
                             ('/api/reviews/session?id=a&id=b',400),
                             ('/api/reviews/session?id=unknown',404),
                             ('/api/reviews/message?id=github%3Adelivery-1&session=s1',400)]:
            handler.path = path; handler.do_GET()
            self.assertEqual(handler.respond.call_args.args[0],status,path)
        handler.headers['Origin'] = 'https://evil.test'
        handler.do_GET(); self.assertEqual(handler.respond.call_args.args[0],403)
        self.assertTrue(all(len(call.args) == 1 for call in self.manager.gh.call_args_list))


if __name__ == '__main__':
    unittest.main()

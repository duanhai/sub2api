import base64
import gzip
import importlib.util
import json
import pathlib
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]


def load_module(name: str, path: pathlib.Path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


extractor = load_module("extractor", ROOT / "extractor" / "index.py")
analyzer = load_module("analyzer", ROOT / "analyzer" / "index.py")


def cls_event(contents):
    payload = {
        "topic_id": "topic-id",
        "topic_name": "topic-name",
        "records": [
            {"timestamp": "1787626800000000", "content": content} for content in contents
        ],
    }
    encoded = base64.b64encode(gzip.compress(json.dumps(payload).encode())).decode()
    return {"clslogs": {"data": encoded}}


class ExtractorTests(unittest.TestCase):
    def test_extracts_chat_history(self):
        raw = {
            "id": "req-1",
            "created_at": "2026-08-26T03:00:00Z",
            "api_key_id": 9,
            "session_id": "session-1",
            "request_body": json.dumps(
                {
                    "model": "gpt-5.6-sol",
                    "messages": [
                        {"role": "user", "content": "Docker 怎么升级？"},
                        {"role": "assistant", "content": "可以滚动更新。"},
                        {"role": "user", "content": "那单机怎么办？"},
                    ],
                },
                ensure_ascii=False,
            ),
            "body_state": "captured",
        }
        candidate = extractor.extract_candidate(json.dumps(raw, ensure_ascii=False))
        self.assertEqual(candidate["request_id"], "req-1")
        self.assertTrue(candidate["has_assistant_history"])
        self.assertIn("user: Docker 怎么升级？", candidate["distill_text"])
        self.assertIn("assistant: 可以滚动更新。", candidate["distill_text"])

    def test_extracts_responses_api_content(self):
        raw = {
            "id": "req-2",
            "request_body": json.dumps(
                {
                    "input": [
                        {
                            "type": "message",
                            "role": "user",
                            "content": [{"type": "input_text", "text": "分析 CLS 日志"}],
                        }
                    ]
                },
                ensure_ascii=False,
            ),
        }
        candidate = extractor.extract_candidate(json.dumps(raw, ensure_ascii=False))
        self.assertEqual(candidate["distill_text"], "user: 分析 CLS 日志")

    def test_lenient_extraction_from_truncated_json(self):
        raw = {
            "id": "req-3",
            "body_state": "truncated",
            "request_body": '{"messages":[{"role":"user","content":"排查白页"},{"role":"assistant","content":"查看控制台"},{"role":"user","content":"发现接口正常"}',
        }
        candidate = extractor.extract_candidate(json.dumps(raw, ensure_ascii=False))
        self.assertIn("排查白页", candidate["distill_text"])
        self.assertIn("查看控制台", candidate["distill_text"])
        self.assertTrue(candidate["has_assistant_history"])

    def test_decodes_cls_event(self):
        records = extractor.decode_cls_records(cls_event(["one", "two"]))
        self.assertEqual([record["content"] for record in records], ["one", "two"])

    def test_diagnostic_does_not_include_content_values(self):
        diagnostic = extractor.describe_record(
            {"timestamp": "1", "content": '{"request_body":"secret prompt","api_key_id":9}'}
        )
        rendered = json.dumps(diagnostic)
        self.assertNotIn("secret prompt", rendered)
        self.assertEqual(diagnostic["decoded_keys"], ["api_key_id", "request_body"])
        self.assertEqual(diagnostic["request_body_length"], len("secret prompt"))
        self.assertFalse(diagnostic["request_body_valid_json"])


class AnalyzerTests(unittest.TestCase):
    def test_parses_candidate_from_scf_log_line(self):
        candidate = {"request_id": "req-1", "distill_text": "user: hello"}
        line = "START RequestId: abc\n" + analyzer.CANDIDATE_MARKER + json.dumps(candidate)
        self.assertEqual(
            analyzer.parse_candidate(line),
            {**candidate, "has_assistant_history": False},
        )

    def test_parses_candidate_from_cls_processing_record(self):
        candidate = {
            "candidate_version": "1",
            "request_id": "req-2",
            "api_key_id": "9",
            "distill_text": '{"role":"assistant","content":"先检查日志"}',
        }
        parsed = analyzer.parse_candidate(json.dumps(candidate, ensure_ascii=False))
        self.assertEqual(parsed["request_id"], "req-2")
        self.assertTrue(parsed["has_assistant_history"])

    def test_rejects_non_candidate_json(self):
        self.assertIsNone(analyzer.parse_candidate('{"message":"ordinary log"}'))

    def test_groups_by_api_key_and_session(self):
        candidates = [
            {
                "request_id": "req-1",
                "source": "sub",
                "api_key_id": 1,
                "session_id": "s1",
                "created_at": "2026-08-26T03:00:00Z",
                "distill_text": "user: first",
            },
            {
                "request_id": "req-2",
                "source": "sub",
                "api_key_id": 1,
                "session_id": "s1",
                "created_at": "2026-08-26T03:01:00Z",
                "distill_text": "user: second",
            },
        ]
        groups = analyzer.group_candidates(candidates)
        self.assertEqual(len(groups), 1)
        self.assertEqual(len(next(iter(groups.values()))), 2)

    def test_fallback_groups_by_five_minute_window(self):
        candidates = [
            {
                "source": "sub",
                "api_key_id": 1,
                "created_at": "2026-08-26T03:01:00Z",
                "distill_text": "user: first",
            },
            {
                "source": "sub",
                "api_key_id": 1,
                "created_at": "2026-08-26T03:04:59Z",
                "distill_text": "user: second",
            },
        ]
        self.assertEqual(len(analyzer.group_candidates(candidates)), 1)

    def test_response_output_text(self):
        response = {"output": [{"content": [{"type": "output_text", "text": "{\"ok\":true}"}]}]}
        self.assertEqual(analyzer.response_output_text(response), '{"ok":true}')


if __name__ == "__main__":
    unittest.main()

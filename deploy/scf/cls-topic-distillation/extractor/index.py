from __future__ import annotations

import base64
import gzip
import json
import os
import re
from typing import Any


CANDIDATE_MARKER = "SUB2API_DISTILL_CANDIDATE "
DIAGNOSTIC_MARKER = "SUB2API_DISTILL_DIAGNOSTIC "
DEFAULT_TEXT_LIMIT = 4000
ROLE_PATTERN = re.compile(r'"role"\s*:\s*"(user|assistant)"', re.IGNORECASE)
STRING_FIELD_PATTERN = re.compile(
    r'"(?:text|content)"\s*:\s*("(?:\\.|[^"\\])*")', re.DOTALL
)


def main_handler(event: dict[str, Any], _context: Any) -> dict[str, int]:
    records = decode_cls_records(event)
    emitted = 0
    for record in records:
        candidate = extract_candidate(record.get("content", ""), record.get("timestamp", ""))
        if candidate is None:
            continue
        print(CANDIDATE_MARKER + json.dumps(candidate, ensure_ascii=False, separators=(",", ":")))
        emitted += 1
    if records and emitted == 0:
        diagnostics = [describe_record(record) for record in records[:10]]
        print(DIAGNOSTIC_MARKER + json.dumps(diagnostics, separators=(",", ":")))
    return {"processed": len(records), "emitted": emitted}


def decode_cls_records(event: dict[str, Any]) -> list[dict[str, Any]]:
    encoded = event.get("clslogs", {}).get("data", "")
    if not isinstance(encoded, str) or not encoded:
        return []
    try:
        payload = gzip.decompress(base64.b64decode(encoded))
        decoded = json.loads(payload)
    except (ValueError, TypeError, OSError, json.JSONDecodeError):
        return []
    records = decoded.get("records", []) if isinstance(decoded, dict) else []
    return [record for record in records if isinstance(record, dict)]


def extract_candidate(content: Any, timestamp: Any = "") -> dict[str, Any] | None:
    raw = parse_log_content(content)
    if not raw:
        return None
    request_body = raw.get("request_body")
    if not isinstance(request_body, str) or not request_body.strip():
        return None

    turns = extract_turns(request_body)
    if not turns:
        return None
    text_limit = env_int("DISTILL_TEXT_MAX_CHARS", DEFAULT_TEXT_LIMIT, 500, 12000)
    distill_text = trim_from_end("\n\n".join(f"{role}: {text}" for role, text in turns), text_limit)
    if not distill_text:
        return None

    return compact_dict(
        {
            "request_id": raw.get("id") or raw.get("local_request_id"),
            "created_at": raw.get("created_at"),
            "timestamp": str(timestamp or ""),
            "source": raw.get("source"),
            "api_key_id": raw.get("api_key_id"),
            "api_key_name": raw.get("api_key_name"),
            "user_id": raw.get("user_id"),
            "user_email": raw.get("user_email"),
            "session_id": raw.get("session_id"),
            "model": raw.get("model"),
            "body_state": raw.get("body_state"),
            "has_assistant_history": any(role == "assistant" for role, _ in turns),
            "distill_text": distill_text,
        }
    )


def parse_log_content(content: Any) -> dict[str, Any]:
    if isinstance(content, dict):
        return content
    if not isinstance(content, str):
        return {}
    value = content.strip()
    try:
        decoded = json.loads(value)
        return decoded if isinstance(decoded, dict) else {}
    except json.JSONDecodeError:
        start = value.find("{")
        if start < 0:
            return {}
        try:
            decoded = json.loads(value[start:])
            return decoded if isinstance(decoded, dict) else {}
        except json.JSONDecodeError:
            return {}


def describe_record(record: dict[str, Any]) -> dict[str, Any]:
    content = record.get("content")
    description: dict[str, Any] = {
        "record_keys": sorted(str(key) for key in record.keys()),
        "content_type": type(content).__name__,
    }
    if isinstance(content, str):
        description.update(
            {
                "content_length": len(content),
                "starts_with_object": content.lstrip().startswith("{"),
                "contains_request_body_key": '"request_body"' in content,
            }
        )
        try:
            decoded = json.loads(content)
        except json.JSONDecodeError:
            description["decoded_type"] = "invalid_json"
        else:
            description["decoded_type"] = type(decoded).__name__
            if isinstance(decoded, dict):
                description["decoded_keys"] = sorted(str(key) for key in decoded.keys())
                request_body = decoded.get("request_body")
                if isinstance(request_body, str):
                    description.update(
                        {
                            "request_body_length": len(request_body),
                            "request_body_valid_json": is_json(request_body),
                            "request_body_has_user_role": bool(ROLE_PATTERN.search(request_body)),
                            "extracted_turn_count": len(extract_turns(request_body)),
                        }
                    )
            elif isinstance(decoded, list) and decoded and isinstance(decoded[0], dict):
                description["first_item_keys"] = sorted(str(key) for key in decoded[0].keys())
    elif isinstance(content, dict):
        description["content_keys"] = sorted(str(key) for key in content.keys())
    return description


def is_json(value: str) -> bool:
    try:
        json.loads(value)
        return True
    except json.JSONDecodeError:
        return False


def extract_turns(request_body: str) -> list[tuple[str, str]]:
    try:
        payload = json.loads(request_body)
    except json.JSONDecodeError:
        return extract_turns_lenient(request_body)

    turns: list[tuple[str, str]] = []
    collect_role_turns(payload, turns)
    if turns:
        return deduplicate_adjacent(turns)

    if isinstance(payload, dict):
        for key in ("input", "prompt", "query"):
            value = payload.get(key)
            if isinstance(value, str) and value.strip():
                return [("user", clean_text(value))]
    return []


def collect_role_turns(value: Any, turns: list[tuple[str, str]]) -> None:
    if isinstance(value, list):
        for item in value:
            collect_role_turns(item, turns)
        return
    if not isinstance(value, dict):
        return

    role = str(value.get("role", "")).strip().lower()
    if role in {"user", "assistant"}:
        text = content_text(value.get("content"))
        if not text:
            text = content_text(value.get("text"))
        if text:
            turns.append((role, text))
        return

    for key in ("messages", "input", "conversation", "contents"):
        if key in value:
            collect_role_turns(value[key], turns)


def content_text(value: Any) -> str:
    if isinstance(value, str):
        return clean_text(value)
    if isinstance(value, list):
        parts: list[str] = []
        for item in value:
            if isinstance(item, str):
                parts.append(item)
            elif isinstance(item, dict):
                item_type = str(item.get("type", ""))
                if item_type in {"text", "input_text", "output_text"} or "text" in item:
                    text = item.get("text")
                    if isinstance(text, str):
                        parts.append(text)
        return clean_text("\n".join(parts))
    if isinstance(value, dict):
        text = value.get("text")
        return clean_text(text) if isinstance(text, str) else ""
    return ""


def extract_turns_lenient(request_body: str) -> list[tuple[str, str]]:
    matches = list(ROLE_PATTERN.finditer(request_body))
    turns: list[tuple[str, str]] = []
    for index, match in enumerate(matches):
        end = matches[index + 1].start() if index + 1 < len(matches) else len(request_body)
        segment = request_body[match.end():end]
        texts: list[str] = []
        for value_match in STRING_FIELD_PATTERN.finditer(segment):
            try:
                decoded = json.loads(value_match.group(1))
            except json.JSONDecodeError:
                continue
            if isinstance(decoded, str) and decoded.strip():
                texts.append(decoded)
        text = clean_text("\n".join(texts))
        if text:
            turns.append((match.group(1).lower(), text))
    return deduplicate_adjacent(turns)


def deduplicate_adjacent(turns: list[tuple[str, str]]) -> list[tuple[str, str]]:
    result: list[tuple[str, str]] = []
    for turn in turns:
        if not result or result[-1] != turn:
            result.append(turn)
    return result


def clean_text(value: str) -> str:
    return " ".join(value.replace("\x00", " ").split()).strip()


def trim_from_end(value: str, limit: int) -> str:
    value = value.strip()
    if len(value) <= limit:
        return value
    return value[-limit:].lstrip()


def compact_dict(value: dict[str, Any]) -> dict[str, Any]:
    return {key: item for key, item in value.items() if item not in (None, "")}


def env_int(name: str, default: int, minimum: int, maximum: int) -> int:
    try:
        value = int(os.getenv(name, str(default)))
    except ValueError:
        return default
    return max(minimum, min(maximum, value))

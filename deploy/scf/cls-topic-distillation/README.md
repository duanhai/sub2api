# CLS topic distillation

This pipeline keeps model analysis outside the Sub2API process.

```text
sub2api-request-details
  -> CLS data processing (extract and cap the request tail)
  -> sub2api-request-candidates
  -> CLS trigger (300 seconds)
  -> sub2api-cls-topic-analyzer
  -> analyzer SCF log topic (structured topic analyses)
```

CLS data processing keeps only the request metadata and a bounded `distill_text` field, so the raw request body never enters an SCF event. The extractor remains as a local parser and fallback, but is not used on the production path because CLS-to-SCF events are limited to 128 KB.

The analyzer accepts both data-processing JSON records and legacy extractor marker lines. It groups candidates by source, API key, and session. If no session is present, it uses a five-minute bucket. It calls an OpenAI-compatible Responses endpoint once per group and prints lines prefixed with `SUB2API_TOPIC_ANALYSIS`.

## Production data processing

Singapore task: `sub2api-request-candidate-extract`

Task ID: `4c1201d0-b9a0-4af4-992d-0553e2692309`

```text
log_keep(has_field("request_body"))
fields_set("candidate_version", "1", "request_id", v("id"), "distill_text", regex_select(v("request_body"), regex="(?s)(.{0,4000})$", index=0, group=1))
fields_keep("candidate_version", "request_id", "created_at", "source", "api_key_id", "api_key_name", "user_id", "user_email", "session_id", "model", "body_state", "distill_text")
log_output("candidates")
```

The task starts from `2026-08-26 12:30:35` Asia/Taipei and runs continuously. Its fixed output alias `candidates` points to `sub2api-request-candidates`.

## SCF handlers

- Extractor: `index.main_handler`
- Analyzer: `index.main_handler`

Both functions use only the Python standard library.

## Environment

Extractor:

- `DISTILL_TEXT_MAX_CHARS=4000`

Analyzer:

- `OPENAI_BASE_URL`
- `OPENAI_API_KEY`
- `TOPIC_SUMMARY_MODEL=gpt-5.6-luna`
- `DISTILL_GROUP_MAX_CHARS=12000`

Never commit or print `OPENAI_API_KEY`. The analyzer derives the existing internal-request marker from the key so its model calls are excluded from Sub2API request-detail capture.

## Tests

```bash
python3 -m unittest discover -s deploy/scf/cls-topic-distillation/tests -v
```

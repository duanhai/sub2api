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

Sub2API can now emit `current_user_text` and the client-resubmitted `previous_assistant_text` directly. CLS data processing keeps those fields and request metadata, so the raw request body never enters an SCF event. The extractor remains compatible with legacy raw rows and is not used on the production path because CLS-to-SCF events are limited to 128 KB.

The analyzer accepts both data-processing JSON records and legacy extractor marker lines. It groups candidates by source, API key, and session. If no session is present, it uses a five-minute bucket. It calls an OpenAI-compatible Responses endpoint once per group and prints lines prefixed with `SUB2API_TOPIC_ANALYSIS`.

## Production data processing

Singapore task: `sub2api-request-candidate-extract`

Task ID: `4c1201d0-b9a0-4af4-992d-0553e2692309`

```text
log_keep(has_field("current_user_text"))
fields_set("candidate_version", "2", "request_id", v("id"))
fields_keep("candidate_version", "request_id", "created_at", "source", "api_key_id", "api_key_name", "user_id", "user_email", "session_id", "model", "body_state", "current_user_text", "previous_assistant_text", "assistant_source", "assistant_lag", "conversation_text_state", "conversation_extract_state")
log_output("candidates")
```

Roll out with `SUB2API_REQUEST_DETAIL_LOG_MODE=dual`, switch the data-processing task to the structured fields above, and use `structured` only after candidate volume and content have been verified. In `structured` mode, raw bodies are omitted only for successful `/responses` requests with a captured conversation; failures and unsupported routes retain the bounded raw fallback. `raw` remains the default.

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

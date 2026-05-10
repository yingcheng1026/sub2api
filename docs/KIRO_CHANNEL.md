# Kiro Channel

## Scope

Kiro is an independent platform and account pool. Kiro accounts must be added as `platform=kiro` and `type=apikey`, then assigned only to Kiro groups. The shared `/v1` surface still rejects Kiro groups unless `kiro.auto_route_on_v1` is explicitly enabled in a later rollout.

## Runtime Flow

```text
client key in Kiro group
  -> /kiro/v1/messages|responses|chat/completions
  -> Sub2API selects only Kiro accounts in that group
  -> Sub2API forwards selected upstream account api_key to Kiro sidecar as X-Kiro-API-Key
  -> sidecar calls Kiro CLI/API
  -> Sub2API records usage from sidecar usage fields when present
```

## Config

```yaml
kiro:
  enabled: true
  route_enabled: true
  auto_route_on_v1: false
  sidecar_url: "http://127.0.0.1:8787"
  max_concurrency: 1
  request_timeout_seconds: 90
```

Defaults keep all Kiro routes disabled. `max_concurrency` is a process-wide sidecar cap, separate from per-user and per-account concurrency.

## Sidecar Contract

Required:

- `GET /healthz`
- `GET /v1/models`
- `POST /v1/messages`
- `POST /v1/chat/completions`
- `POST /v1/responses`

Sub2API sends:

- `X-Kiro-API-Key`: selected Kiro upstream account API key
- `X-Kiro-Account-ID`: selected account ID
- `X-Kiro-Original-Path`: original client path
- `X-Request-ID`: client request ID when present

The sidecar should return normal JSON responses and may include common usage shapes:

- Anthropic style: `usage.input_tokens`, `usage.output_tokens`
- OpenAI style: `usage.prompt_tokens`, `usage.completion_tokens`
- Gemini style: `usage.promptTokenCount`, `usage.candidatesTokenCount`

## First Test

1. Start the reference sidecar in `tools/kiro-sidecar`.
2. Set `kiro.enabled=true`, `kiro.route_enabled=true`, and `kiro.sidecar_url`.
3. Create a Kiro group.
4. Add the upstream Kiro account as Kiro API Key and assign it to that group.
5. Create or use an API key assigned to the Kiro group.
6. Test:

```bash
curl -sS "$BASE_URL/kiro/v1/messages" \
  -H "authorization: Bearer $SUB2API_KEY" \
  -H "content-type: application/json" \
  -d '{"model":"kiro","messages":[{"role":"user","content":"hi"}],"stream":false}'
```

Production deploy should still be a canary: enable the sidecar and routes for an internal Kiro group first, verify health/usage, then add customer-facing Kiro groups.

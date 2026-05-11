# Kiro Channel

## Scope

Kiro is a Claude-compatible upstream platform. Kiro accounts must be added as `platform=kiro` and `type=apikey`. They can stay in Kiro groups for dedicated `/kiro/v1` canaries, or be assigned to Anthropic groups to participate in the shared `/v1/messages`, `/v1/chat/completions`, and `/v1/responses` Claude pool.

## Runtime Flow

```text
client key in Kiro group
  -> /kiro/v1/messages|responses|chat/completions
  -> Sub2API selects Kiro accounts in that group
  -> Sub2API forwards selected upstream account api_key to Kiro sidecar as X-Kiro-API-Key
  -> sidecar refreshes the Kiro credential and calls Kiro/AWS generateAssistantResponse
  -> sidecar parses AWS event-stream frames into Anthropic/OpenAI-compatible output
  -> Sub2API records usage from sidecar usage fields when present

client key in Anthropic group with Kiro accounts
  -> /v1/messages|responses|chat/completions
  -> Sub2API includes Kiro accounts as Claude-compatible candidates
  -> Sub2API forwards selected upstream account api_key to Kiro sidecar as X-Kiro-API-Key
  -> sidecar refreshes the Kiro credential and calls Kiro/AWS generateAssistantResponse
  -> sidecar parses AWS event-stream frames into Anthropic/OpenAI-compatible output
  -> Sub2API records usage from sidecar usage fields when present
```

## Interface Decision

After comparing the public Kiro implementations, the recommended production path is a direct HTTP sidecar, not a per-request `kiro-cli` shell wrapper.

| Reference | Useful finding | Decision |
| --- | --- | --- |
| `kiro-gateway` | Full FastAPI gateway, Kiro Desktop/AWS SSO refresh, model resolver, AWS event-stream parsing, retries. AGPL-3.0. | Use as architecture reference only; do not import code. |
| `Kiro-Go` | MIT Go gateway with dual CodeWhisperer/AmazonQ endpoints, account pool, model mapping, event-stream parser. | Best protocol reference; reimplemented the needed behavior in our Node sidecar. |
| `Kiro-account-manager` | Confirms account-manager credential shapes, local refresh flow, proxy API, model list behavior. | Use for admin/account operational clues. |
| `AIClient-2-API` | Confirms Kiro headers, token refresh endpoints, provider conversion behavior. GPLv3. | Use as cross-check only; do not import code. |
| `CLIProxyAPIPlus` | Repository was not publicly readable during review. | Excluded from implementation choice until readable. |

The resulting contract keeps Kiro explicit while allowing Claude-pool fusion:

- Kiro remains `platform=kiro` at the account layer, so usage logs and upstream behavior remain attributable.
- Kiro groups stay Kiro-only; Anthropic groups may include Kiro accounts because Kiro exposes only Claude-compatible model IDs.
- The normal `/v1` surface still rejects API keys assigned directly to Kiro groups while `kiro.auto_route_on_v1=false`; Anthropic groups with Kiro accounts use the shared Claude pool.
- The sidecar remains a separate local process with its own concurrency cap and can be stopped without affecting current customer traffic.
- Direct mode supports streaming and tool calls; CLI mode remains only a non-stream fallback for canary debugging.

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
- `POST /v1/messages/count_tokens`
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

### Upstream credential format

The admin account still uses the ordinary API key field. For direct mode, store either a raw Kiro Desktop `refreshToken` or a JSON credential:

```json
{
  "authType": "desktop",
  "refreshToken": "xxx",
  "region": "us-east-1",
  "machineId": "optional"
}
```

For AWS SSO OIDC / kiro-cli style credentials:

```json
{
  "authType": "aws_sso_oidc",
  "refreshToken": "xxx",
  "clientId": "xxx",
  "clientSecret": "xxx",
  "region": "us-east-1",
  "ssoRegion": "us-east-1"
}
```

The JSON may be pasted directly, prefixed with `json:`, or base64url encoded with `base64url:`. The sidecar caches refreshed access tokens in memory only.

## First Test

1. Start the reference sidecar in `tools/kiro-sidecar`.
2. Set `kiro.enabled=true`, `kiro.route_enabled=true`, and `kiro.sidecar_url`.
3. Create a Kiro group for isolated canary, or choose an Anthropic/Claude group for fused scheduling.
4. Add the upstream Kiro account as Kiro API Key and assign it to the chosen group.
5. Create or use an API key assigned to that group.
6. Test:

```bash
curl -sS "$BASE_URL/kiro/v1/messages" \
  -H "authorization: Bearer $SUB2API_KEY" \
  -H "content-type: application/json" \
  -d '{"model":"kiro","messages":[{"role":"user","content":"hi"}],"stream":false}'
```

Production deploy should still be a canary: enable the sidecar and routes for an internal Kiro group first, verify health/usage, then add customer-facing Kiro groups.

# Kiro Sidecar

This sidecar is the isolated Kiro upstream adapter for the Sub2API Kiro channel. Sub2API never sends the end-user API key to Kiro directly; it selects a Kiro upstream account and passes that account's `api_key` to this sidecar as `X-Kiro-API-Key`.

The preferred mode is direct Kiro HTTP: the sidecar refreshes the upstream Kiro credential, calls Kiro/AWS `generateAssistantResponse`, parses AWS event-stream frames, and returns Anthropic/OpenAI-compatible responses. `kiro-cli` remains available only as a canary fallback.

## Start locally

```bash
cd tools/kiro-sidecar
HOST=127.0.0.1 PORT=8787 KIRO_SIDECAR_MODE=auto node server.mjs
```

Modes:

- `KIRO_SIDECAR_MODE=auto`: try direct HTTP first, then fall back to CLI for non-stream requests.
- `KIRO_SIDECAR_MODE=direct`: direct HTTP only.
- `KIRO_SIDECAR_MODE=cli`: legacy CLI wrapper only.

If you need CLI fallback and your Kiro CLI command differs, set `KIRO_CLI_ARGS_JSON`:

```bash
KIRO_CLI_ARGS_JSON='["chat","--no-interactive","--model","{model}","{prompt}"]' node server.mjs
```

By default, `/v1/models` exposes the same Claude-only model IDs that Sub2API exposes for Kiro accounts: Anthropic's current latest Opus, Sonnet, and Haiku IDs for Claude Code/API use. Override `KIRO_MODELS` only if the production Kiro upstream catalog changes.

## Upstream account credential

The Sub2API Kiro account should still be added as `platform=kiro` and `type=apikey`. Put one of these values in the account API key field:

Raw long-lived Kiro Desktop refresh token:

```text
<refreshToken>
```

JSON credential, pasted directly or prefixed with `json:`:

```json
{
  "authType": "desktop",
  "refreshToken": "xxx",
  "region": "us-east-1",
  "machineId": "optional-machine-id"
}
```

AWS SSO OIDC credential:

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

The JSON can also be base64url encoded and prefixed with `base64url:` if the admin UI makes multiline JSON inconvenient. The sidecar caches refreshed access tokens in memory, but does not write refreshed credentials back to disk or the database.

## Sub2API config

```yaml
kiro:
  enabled: true
  route_enabled: true
  auto_route_on_v1: false
  sidecar_url: "http://127.0.0.1:8787"
  max_concurrency: 1
  request_timeout_seconds: 90
```

Keep `auto_route_on_v1: false` during canary. Test through `/kiro/v1/messages`, `/kiro/v1/chat/completions`, or `/kiro/v1/responses`.

## Smoke test

```bash
curl http://127.0.0.1:8787/healthz
curl -sS http://127.0.0.1:8787/v1/models
curl -sS http://127.0.0.1:8787/v1/messages \
  -H 'content-type: application/json' \
  -H 'x-kiro-api-key: json:{"authType":"desktop","refreshToken":"xxx","region":"us-east-1"}' \
  -d '{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":false}'
```

Streaming is supported for `/v1/messages`, `/v1/chat/completions`, and `/v1/responses` in direct mode. For Claude Code canaries, use `/kiro/v1/messages` with a Kiro-group API key first, then only enable wider routing after health, account failover, and usage logs look clean.

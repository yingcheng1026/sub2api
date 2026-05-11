# Kiro Sidecar

This is a minimal local sidecar for the Sub2API Kiro channel. Sub2API never sends the end-user API key to Kiro directly; it selects a Kiro upstream account and passes that account's `api_key` to this sidecar as `X-Kiro-API-Key`.

## Start locally

```bash
cd tools/kiro-sidecar
HOST=127.0.0.1 PORT=8787 KIRO_CLI_PATH=kiro-cli node server.mjs
```

If your Kiro CLI command differs, set `KIRO_CLI_ARGS_JSON`:

```bash
KIRO_CLI_ARGS_JSON='["chat","--no-interactive","--model","{model}","{prompt}"]' node server.mjs
```

By default, `/v1/models` exposes the same Claude-only model IDs that Sub2API exposes for Kiro accounts. Override `KIRO_MODELS` only if the production Kiro upstream catalog changes.

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
  -H 'x-kiro-api-key: kiro_xxx' \
  -d '{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":false}'
```

The reference sidecar intentionally does not implement streaming. It returns a normal JSON response so the admin account test and first `/kiro/v1/messages` can verify account usability before a production-grade sidecar is deployed.

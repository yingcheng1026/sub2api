# HFC GPT-5.6 客户接入说明

仅 `openai-default` 分组可以使用 GPT-5.6。现有客户不需要更换 API Key 或 Base URL；非授权分组返回 `403`，裸 `gpt-5.6` 返回 `400`。升级 Codex 是建议步骤，但不会自动切换模型，仍需显式填写 exact tier。

## 可用模型

GPT-5.6 只接受以下 exact tier，不接受裸 `gpt-5.6`：

以下是服务端内置基础价格快照，用于请求前计价和历史账务重建；客户最终实扣还会叠加实际生效的分组/用户倍率，不应把本表单独当作客户报价。

| 模型 | 定位 | 基础输入 / 1M tokens | 基础输出 / 1M tokens | 基础 Cache write / 1M | 基础 Cache read / 1M |
|---|---|---:|---:|---:|---:|
| `gpt-5.6-sol` | 高能力 | $5.00 | $30.00 | $6.25 | $0.50 |
| `gpt-5.6-terra` | 平衡，默认推荐 | $2.50 | $15.00 | $3.125 | $0.25 |
| `gpt-5.6-luna` | 经济 | $1.00 | $6.00 | $1.25 | $0.10 |

现有 `claude-*` alias 继续使用原映射，不会静默切换到 GPT-5.6。要使用 GPT-5.6，客户端必须直接填写上表中的 exact model ID。Legacy alias 只代表兼容身份，日志主模型、上游模型和计费模型显示真实执行的 GPT。

## Claude Code

Claude Code 继续使用 Anthropic Messages 协议，但模型填写真实 GPT 名称：

```text
ANTHROPIC_BASE_URL=https://api.handsfreeclub.com
ANTHROPIC_AUTH_TOKEN=<HFC key>
ANTHROPIC_MODEL=gpt-5.6-terra
ANTHROPIC_CUSTOM_MODEL_OPTION=gpt-5.6-terra
ANTHROPIC_DEFAULT_OPUS_MODEL=gpt-5.6-sol
ANTHROPIC_DEFAULT_SONNET_MODEL=gpt-5.6-terra
ANTHROPIC_DEFAULT_HAIKU_MODEL=gpt-5.6-luna
CLAUDE_CODE_SUBAGENT_MODEL=inherit
```

Windows 客户建议使用 HFC 页面生成的 PowerShell 配置。持久化后必须重新打开 Windows Terminal，再启动 Claude Code。

这里的角色对应关系是透明且按成本分层的：Opus 使用 Sol、Sonnet 和默认模型使用 Terra、Haiku 使用 Luna；usage 仍记录真实 GPT exact tier，不记录成 Claude 模型。

## Codex

`~/.codex/config.toml`，Windows 对应 `%USERPROFILE%\.codex\config.toml`：

```toml
model_provider = "handsfree"
model = "gpt-5.6-terra"
review_model = "gpt-5.6-terra"
model_reasoning_effort = "high"
disable_response_storage = true

[model_providers.handsfree]
name = "Handsfree Club"
base_url = "https://api.handsfreeclub.com/v1"
wire_api = "responses"
requires_openai_auth = true
```

`~/.codex/auth.json`（Windows 对应 `%USERPROFILE%\.codex\auth.json`）：

```json
{
  "OPENAI_API_KEY": "<HFC key>"
}
```

自定义 provider 必须放在用户级 `~/.codex/config.toml`，不要放进项目级 `.codex/config.toml`。建议先运行 `codex update`，旧安装也可用 `npm i -g @openai/codex@latest` 升级。

首发默认使用 HTTP Responses，不默认打开 WebSocket。

## OpenAI SDK / HTTP

Base URL：

```text
https://api.handsfreeclub.com/v1
```

最小请求：

```bash
curl https://api.handsfreeclub.com/v1/responses \
  -H "Authorization: Bearer $HFC_API_KEY" \
  -H "Content-Type: application/json" \
  --data '{"model":"gpt-5.6-terra","input":"Reply exactly HFC_GPT56_OK","max_output_tokens":16}'
```

## 身份和计费说明

- Usage 主模型、上游模型和计费模型均显示真实 GPT-5.6 exact tier。
- Claude Code 的兼容身份单独记录，不再把主模型显示成 Claude。
- 没有 exact tier 权限或上游未开放时返回明确 4xx，不应落成网站级 503。
- 费用使用请求前冻结的价格证据；成功 usage 会保存价格 source、revision 和 hash。

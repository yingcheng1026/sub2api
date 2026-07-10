# HFC GPT-5.6 原生 Claude Code 兼容本地验证报告

日期：2026-07-10
分支：`codex/gpt56-native-claude-compat-20260710`
基线：`99276e04f50271a39c0536e8f533b882568e0e2f`
代码候选：`931915e3f2d260081aa6fef69f25690e9f1b85fb`
状态：**本地定向候选通过；整仓验收未全绿；生产禁止部署**

## 1. 范围和边界

本轮只开发、测试和构建本地候选，没有运行候选镜像，没有修改生产数据库、Redis、账号池、API Key、`openai-default` 分组、渠道、费率、钱包、月卡、容器或路由。

目标链路：

```text
Claude Code / OpenAI 客户端
  -> HFC openai-default
  -> requested model / channel mapping / Claude legacy alias dispatch
  -> final routing model
  -> exact account capability
  -> upstream model
  -> exact pricing candidate
  -> billing_model audit
  -> admin Usage identity display/export
```

## 2. 候选行为

| 区块 | 本地候选行为 |
|---|---|
| GPT-5.6 ID | 只接受 `gpt-5.6-sol`、`gpt-5.6-terra`、`gpt-5.6-luna` 及允许的 reasoning suffix |
| 未知 5.6 | 裸 `gpt-5.6`、未知 tier、畸形 family 均 fail closed，不回退 GPT-5.4 |
| 默认目录 | GPT-5.6 暂不进入默认客户模型目录，避免未完成 canary 就公开 |
| Claude Code 新配置 | Anthropic 协议继续使用；模型直接填原生 GPT，不再伪装成 Claude |
| 稳定默认模型 | main/opus=`gpt-5.5`、sonnet=`gpt-5.4`、haiku=`gpt-5.4-mini`、subagent=`inherit` |
| Legacy 兼容 | `claude-*` 和 bare `opus/sonnet/haiku/default` 继续作为兼容 alias；审计明确标记 `legacy_claude_alias` |
| Usage 主模型 | 主行显示实际执行模型，另外标注 requested/upstream/billing/compat/mapping |
| 计费审计 | 成功命中的价格候选写入 nullable `usage_logs.billing_model`；历史行不回填 |
| 映射安全 | GPT-5.6 只允许同 tier exact target；空值、跨 tier、反向降级、畸形 family 和不支持的 suffix 在上游请求前 fail closed |
| Reasoning suffix | 仅 `none/low/medium/high/xhigh`；调度、HTTP Responses/Chat/passthrough/compact 和 WebSocket 均统一落到账号 exact target，同时保留显式 effort 优先级 |
| WebSocket 边界 | 仅允许同一 canonical 模型的 reasoning suffix 变化；GPT-5.6 通道映射要求 exact 字符串，跨模型 WS mapping 在握手前 1008 fail closed；非 JSON binary 帧拒绝 |

## 3. GPT-5.6 基础价格

| 模型 | Input / 1M | Output / 1M | Cache write / 1M | Cache read / 1M |
|---|---:|---:|---:|---:|
| `gpt-5.6-sol` | $5.00 | $30.00 | $6.25 | $0.50 |
| `gpt-5.6-terra` | $2.50 | $15.00 | $3.125 | $0.25 |
| `gpt-5.6-luna` | $1.00 | $6.00 | $1.25 | $0.10 |

候选代码将三档基础价锁为本地审定值，不接受运行时远程同名条目覆盖；每次读取返回副本，避免调用方污染全局价格对象。

价格来源：[OpenAI 2026-06-26 GPT-5.6 发布页](https://openai.com/index/previewing-gpt-5-6-sol/)。2026-07-10 的 [OpenAI GPT-5.6 Help 页面](https://help.openai.com/en/articles/20001325-a-preview-of-gpt-5-6-sol-terra-and-luna) 已把 API 可用模型列为 Sol/Terra/Luna，但这不证明 HFC 现有每个上游账号已具备三档 entitlement；真实账号能力仍需逐档 canary。

## 4. 数据库兼容

迁移 172 只执行：

```sql
ALTER TABLE usage_logs
ADD COLUMN IF NOT EXISTS billing_model VARCHAR(100);
```

该列 nullable、无默认值、无索引、无历史回填。旧二进制使用显式列写入，可容忍额外列；未来应用回滚时应保留该列，不建议常规 `DROP COLUMN`。

## 5. 验证矩阵

| 验证面 | Fresh 结果 | 最终状态 |
|---|---|---|
| Go：OpenAI/apicompat/service/repository/handler/DTO | 最终 SHA `-count=1` 全部通过；service `85.178s`、repository `1.468s`、handler `21.890s`、DTO `0.012s` | PASS |
| Migration integration | 幂等新库 + 历史 schema 升级两项通过，package `4.783s` | PASS |
| Frontend changed tests | 4 files / 16 tests，`3.04s` | PASS |
| Frontend typecheck/lint/build | 全部通过；Vite 808 modules，`16.12s` | PASS，只有既有 warning |
| Claude Code 2.1.204 | 最终 SHA 重跑 12/12，`3.23s` | PASS |
| Source static security | private key/AWS key/非测试 `sk-*`、敏感路径、deploy diff、Usage `v-html`、diff-check | PASS |
| Full frontend suite | 96 files / 586 tests 通过；6 files / 7 tests 失败、1 unhandled error | FAIL，整仓绿灯不成立 |

Fresh Go 命令：

```bash
docker run --rm --cpus=2 --memory=5g -e GOMAXPROCS=2 -e GOGC=30 \
  -v "$PWD/backend:/src:ro" -v hfc-go-mod-cache:/go/pkg/mod \
  -v hfc-go-build-cache:/root/.cache/go-build -w /src golang:1.26.3 \
  go test -count=1 -p=1 -tags=unit ./internal/pkg/openai ./internal/pkg/apicompat \
  ./internal/service ./internal/repository ./internal/handler ./internal/handler/dto
```

Fresh migration command使用本地 Docker socket、只读源码、独立 Testcontainers PostgreSQL/Redis；两个目标测试均通过，临时容器由 Ryuk 清理。

Fresh frontend：

```text
vitest changed files: 4/4 files, 16/16 tests passed
vue-tsc --noEmit: passed
eslint: passed
vite build: passed, 808 modules
full vitest: 96 passed files, 6 failed files; 586 passed tests, 7 failed tests; 1 unhandled error
```

Full suite 失败文件为：`EmailVerifyView.spec.ts`、`ModelDistributionChart.spec.ts`、`GroupDistributionChart.spec.ts`、`settings.authSourceDefaults.spec.ts`、`usePersistedPageSize.spec.ts`、`WalletBalanceCard.spec.ts`；另有 `DashboardView.spec.ts` 触发 unhandled render rejection。它们均不在 `99276e04..931915e3` 的本轮 diff 中，本轮没有顺手修改。

最终 guard 另外以 fresh 定向用例验证了 channel mapping 优先级、同 tier transition、billing fallback 副本、compact/passthrough 上游前拒绝、Direct Responses/Chat/WebSocket 的 exact target 与 reasoning effort、provider namespace 防碰撞、future GPT 不泛化、binary JSON 帧防绕过和 GPT-5.6 WS channel exact 边界；最终 targeted service `0.039s`、handler `0.015s`；`go vet ./internal/service ./internal/handler`、`gofmt`、`git diff --check` 均通过。

计划中写的 `tools/secret_scan.py` 在仓库不存在，因此该脚本状态是 `信息缺失`；本轮没有虚报它通过，改用可复现的 diff 正则和敏感路径检查。

## 6. 调试假设与证据

### 假设 A：远程价格表污染 exact GPT-5.6

证据：旧实现会优先返回运行时 `pricingData[exact-tier]`；重审构造同名错误价格即可覆盖审定价。修复方向是 GPT-5.6 exact tier 永远返回本地审定价格副本，并用污染测试锁定。

### 假设 B：映射后的 GPT-5.6 绕过账号资格

证据：旧 Responses/Messages/Chat/WebSocket 在选择账号后才替换 channel-mapped model；同时账号 gate 只验 mapping key、不验 value。修复方向是以最终 routing model 选择/复核账号，mapping value 必须规范化后仍为同一 GPT-5.6 tier。

### 假设 C：网页模板与真实 Claude Code 认证/默认模型不一致

证据：旧 harness 主要用 `--model` + `ANTHROPIC_API_KEY`，没有证明页面输出的 `ANTHROPIC_AUTH_TOKEN + ANTHROPIC_MODEL + role defaults + subagent inherit`。补强 harness 必须使用 clean HOME、127.0.0.1 mock、Bearer 且无 `x-api-key`，并真实触发子 Agent 工具链。

### 假设 D：合法 suffix 只影响调度，未影响真实上游 body

证据：旧 guard 可以用归一化 base tier 选到账号，但 Direct Responses/Chat/HTTP passthrough/WebSocket 仍可能转发 raw suffix，从而绕过账号 exact mapping。最终修复将合法 suffix 的 reasoning effort 显式写入协议字段，再把上游 model 改为账号同 tier exact target；compact 及不安全映射在发起上游请求前拒绝。

## 7. 本地镜像

旧候选镜像：

```text
hfc/sub2api:native-gpt-compat-0423bb0b-20260710-1315
sha256:e5b7845162b1c196966f52131ff8232408a3e6e94fad595aa33e9639ffaf2429
linux/arm64
running containers: 0
```

该旧镜像没有 OCI revision，且内容早于最终两项修复提交，不能作为最终可溯源制品，也没有运行。

最终可溯源镜像尝试：

```text
requested tag: hfc/sub2api:native-gpt-compat-b73b126c-20260710-1404
requested COMMIT/OCI revision: b73b126cd8e78d537faf320ab94733d270e89145
SUB2API_IMAGE_CLEANUP=0
SUB2API_BUILDER_GC=0
result: canceled after about 8 minutes with no progress in `go mod download`
exit: 130
image created: no
```

该尝试早于最终 `931915e3` guard，且没有生成镜像。因此最终可溯源镜像是**未完成项**。这不影响源码、Go、迁移、前端定向和 Claude Code 本地验证结论，但阻止把候选称为可发布制品。

## 8. 线上 503 只读排查

2026-07-10 14:25–14:27 CST 对独立生产链路做了只读健康、容器和聚合日志/数据库核对。结论是本地开发不是 503 原因：聚合结果显示同一客户直接请求尚未公开的 `gpt-5.6-terra`，所在付费组当时没有 Terra exact mapping；窗口为 14:05:07–14:19:11。当时 Nginx/Sub2API/PostgreSQL/Redis/model-cache 全部 healthy，公网健康检查 200，容器无 OOM/无重启，最近 5 分钟同类 503 为 0。

该排查未读取客户 prompt、密钥正文或账号凭据，也没有停止、重启、reload 或替换任何生产服务。可提交的脱敏摘要见 `reports/HFC_PRODUCTION_503_TRIAGE_REDACTED_2026-07-10.md`；原始详细记录保留在本地私有位置，不进入产品仓库。

## 9. 生产阻断项

以下不是本地单元测试能替代的内容，全部关闭前生产结论固定为 **NO-GO**：

1. usage/billing 异步队列允许丢任务；扣费成功后的 usage log 仍可能 best-effort 丢失。
2. 缺少上游调用前的完整价格预检；返回结果后才发现计价失败无法撤回。
3. GPT-5.6 preview `403` 目前按整账号冷却/停用，没有模型级隔离。
4. 三档基础 cache 单价已由官方发布页确认，但 HFC Responses→Claude 链路的实际 cache usage 字段、断点、30 分钟生命周期，以及 `service_tier=priority/flex`、长上下文的实际契约均未通过真实 canary。
5. `billing_model` 只有候选名，没有价格来源、价格表 revision/hash 和生效时间，不能单独重建历史价格。
6. 没有真实 HFC 上游三档 entitlement、月卡/钱包/ledger、客户 canary 和生产回归证据。
7. 迁移尚未做生产长事务、锁等待和同名列类型预检。
8. 现存旧候选镜像仅 `linux/arm64`，最终可溯源镜像未产出，也没有生产 AMD64 制品。
9. Windows 修复脚本只有静态检查；本机没有 `pwsh` / Windows PowerShell，实机行为为 `信息缺失`。
10. full frontend suite 仍有继承失败，整仓 green gate 未通过。
11. Responses WS 对于会改变 GPT-5.6 实际 model/suffix 的 channel mapping 明确禁用；这是本地 fail-closed 边界，未实现每轮重新应用 channel mapping。
12. `openai-default.billing_model_source` 生产实际值未做只读核验；如果是 `requested`，现有 RecordUsage 会按渠道政策改回 requested model，必须上线前明确商业意图并做 canary。

## 10. 回滚原则

- 当前没有部署：回滚就是不合并/不发布该分支，候选镜像保持未运行。
- 如果未来已执行 migration 后需要回滚应用，恢复旧镜像 digest，但保留 nullable `billing_model` 列和迁移记录。
- 不常规删列；强制删列会丢审计数据并获取表锁，且必须同步处理迁移记录。
- Windows 修复脚本必须与父仓既有 staged 文件隔离提交；本轮不自动提交父仓。

## 11. 最终判定

- 本地定向代码候选：**PASS**；价格污染、mapping value、四入口 routing、UI template harness、HTTP/WS/compact suffix、provider namespace、future GPT 不泛化和 binary frame 防绕过均已修复并回归。GPT-5.6 跨模型 WS channel mapping 是明确禁用的功能边界，不称为全部 WS 兼容通过。
- 整仓完成门：**INCOMPLETE**；full frontend suite 未全绿，最终可溯源镜像未产出。
- 生产上线：**BLOCKED / NO-GO**。
- No-production scope：当前可观测状态 **PASS**；未运行候选镜像、未 push、未修改生产配置或数据。线上只读排查没有重启、reload、替换或部署任何服务。

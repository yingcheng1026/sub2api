# OpenAI 95% 周限额保护运行中审计

审计时间：2026-05-03 16:23 Asia/Shanghai

## 结论

当前机制已经落地，并且正在生产容器中运行使用。

本次审计只做低负载只读检查，没有执行 Go 测试、没有构建镜像、没有拉依赖、没有清理目录。

## 生产容器状态

Compose 当前镜像：

```text
image: hfc/sub2api:agent-platform-adapters-cdf2e2e65593-20260503-155939
```

运行容器：

```text
name=/sub2api
image=hfc/sub2api:agent-platform-adapters-cdf2e2e65593-20260503-155939
image_id=sha256:a22c96313407233c2c9bd57869aeb95c5bd6cb6100d5b11b4361ed35894e8089
status=running
health=healthy
started=2026-05-03T08:16:55.911638461Z
```

核心容器：

```text
sub2api hfc/sub2api:agent-platform-adapters-cdf2e2e65593-20260503-155939 Up 6 minutes (healthy)
sub2api-postgres postgres:18-alpine Up 7 days (healthy)
sub2api-redis redis:8-alpine Up 7 days (healthy)
```

Health endpoint：

```text
{"status":"ok"}
```

## 机制是否在二进制中

线上容器二进制内确认存在：

```text
hfc-quota-guard:openai-codex-7d>=95
cdf2e2e65593
```

说明当前运行容器包含 95% quota guard 逻辑。

## 是否正在运行使用

最近 10 分钟生产日志中已有真实请求通过当前容器处理成功，包括：

```text
/v1/messages status_code=200
/v1/chat/completions status_code=200
/v1/usage status_code=200
/api/v1/auth/me status_code=200
/api/v1/subscriptions/active status_code=200
```

日志样例显示当前容器已经处理 OpenAI 平台请求：

```text
path=/v1/chat/completions status_code=200 account_id=2 platform=openai model=gpt-5.4
path=/v1/messages status_code=200 account_id=19 platform=openai model=sonnet
path=/v1/messages status_code=200 account_id=15 platform=openai model=sonnet
```

结论：不是只部署未使用，当前容器正在接生产请求。

## DB 调度状态

只读聚合结果：

```text
total_openai_oauth=9
active_schedulable=8
any_at_or_over_95=1
active_schedulable_at_or_over_95_not_guarded=0
hfc_guard_rows=0
```

解释：

- 当前有 9 个 OpenAI OAuth 账号。
- 当前有 8 个 active 且 schedulable。
- 当前有 1 个账号 `codex_7d_used_percent >= 95`。
- 没有任何 `active + schedulable + >=95%` 的账号漏在调度池。
- `hfc_guard_rows=0` 表示当前没有 hfc 临时保护记录；这是因为当前那个 >=95% 账号本身不在可调度状态，不需要新增 hfc guard。

## VPS 关键指标

磁盘：

```text
/dev/vda2 34G used 20G avail 12G use 64%
```

负载：

```text
load average: 0.35, 0.91, 2.12
```

Go 测试容器：

```text
none
```

结论：当前没有测试容器残留，没有构建/测试行为继续影响 VPS 磁盘、CPU 或网站正常运行。

## 测试行为优化状态

已确认并固定：

1. 不在生产 VPS 跑 Go 单测。
2. 不在生产 VPS 拉 Go 依赖。
3. 不在生产 VPS 做完整 Docker 构建。
4. 生产只做最终镜像加载、compose 切换、健康检查和只读 DB 查询。
5. 部署/审计前后都检查磁盘、核心容器和负载。

## 最终判定

| 审计项 | 结果 |
|---|---|
| 机制是否部署到当前线上容器 | 是 |
| 当前容器是否 healthy | 是 |
| 当前容器是否包含 guard 代码 | 是 |
| 当前容器是否正在处理生产请求 | 是 |
| 是否存在 >=95% 可调度漏网账号 | 否 |
| 当前是否有 Go 测试容器影响 VPS | 否 |
| 当前磁盘是否处于 90%+ 危险状态 | 否，当前约 64% |


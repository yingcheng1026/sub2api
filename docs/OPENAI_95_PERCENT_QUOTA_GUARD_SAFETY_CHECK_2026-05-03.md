# OpenAI 95% 周限额保护上线后安全复查

复查时间：2026-05-03 15:56 Asia/Shanghai

## 结论

机制已经落地到生产容器，且测试/验证行为已经优化为低负载模式。

当前生产验证只做：

- 容器状态检查
- `/health` 检查
- Compose 镜像 tag 检查
- 二进制 commit/guard 字符串检查
- `df` / `docker system df` / load 检查
- 只读 DB 聚合查询

不再在生产 VPS 上执行：

- Go 单测
- Go 依赖下载
- 镜像构建
- 大量编译缓存写入
- 任何会拉高磁盘或 CPU 的测试行为

## 生产机制落地状态

当前运行镜像：

```text
hfc/sub2api:openai-quota-guard-50f6911-20260503-1545
sha256:ff6ba3c128b30bd7ecb79ed0d112106aa9ac5d71f2de16f12c288561ee04f9d9
```

Compose 配置：

```text
image: hfc/sub2api:openai-quota-guard-50f6911-20260503-1545
```

容器健康：

```text
sub2api hfc/sub2api:openai-quota-guard-50f6911-20260503-1545 Up 4 minutes (healthy)
sub2api-postgres postgres:18-alpine Up 7 days (healthy)
sub2api-redis redis:8-alpine Up 7 days (healthy)
```

Health endpoint：

```text
{"status":"ok"}
```

二进制内验证：

```text
50f69113dc7ab03bd6c06f6a47b3f5ea54ed4c10
hfc-quota-guard:openai-codex-7d>=95
```

结论：当前线上 `sub2api` 容器确实包含 95% 周限额保护代码。

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

- 当前 OpenAI OAuth 账号总数：9。
- 当前 active 且 schedulable 的 OpenAI OAuth 账号：8。
- 当前有 1 个账号 `codex_7d_used_percent >= 95`。
- 但没有任何 `active + schedulable + >=95%` 的账号漏在调度池里。
- 当前 `hfc_guard_rows=0` 是因为那个 >=95% 账号本身已经不是 schedulable 状态，不需要新增 hfc temp guard。

结论：当前数据库状态没有发现 95% 账号漏调度。

## VPS 关键指标

磁盘：

```text
/dev/vda2 34G used 18G avail 15G use 55%
```

Docker 占用：

```text
Images          27        7         8.709GB   2.632GB reclaimable
Containers      7         7         25.77MB   0B reclaimable
Local Volumes   1         1         0B        0B
Build Cache     44        0         3.291GB   31.18MB reclaimable
```

负载：

```text
load average: 1.15, 2.49, 3.65
```

Go 测试容器：

```text
none
```

结论：当前没有测试容器继续占用 VPS，磁盘已从危险的 90%+ 回落到 55%。

## 已优化的操作规则

以后生产部署/验证必须遵守：

1. 测试在本机或 CI 跑，不在生产 VPS 跑。
2. 镜像在本机或 CI 构建，不在生产 VPS 构建。
3. 生产 VPS 只做最终镜像加载、compose 切换、健康检查和只读 DB 查询。
4. 每次部署前先检查 `df -h`、`docker system df`、核心容器健康。
5. 如磁盘低于 5G 可用空间，不做任何构建/测试行为。
6. 清理只允许清理测试缓存、停止容器、dangling image、Docker build cache；禁止删除数据库卷、Redis/Postgres 数据和当前运行镜像。

## 最终判断

| 项目 | 状态 |
|---|---|
| 95% 周限额保护代码 | 已上线 |
| 生产容器是否包含 guard | 已确认 |
| 当前是否有 >=95% 账号漏调度 | 未发现 |
| 生产测试行为是否还会压 VPS | 已优化为不在 VPS 跑测试 |
| 磁盘是否仍在危险状态 | 当前 55%，已脱离危险状态 |
| 是否仍需人工监督测试行为 | 后续必须按本文件规则执行 |


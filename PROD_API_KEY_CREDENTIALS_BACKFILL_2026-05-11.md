# 生产 API Key 哈希与账号凭证加密 Backfill 记录

日期：2026-05-11 Asia/Shanghai

## 范围

- 核验生产 API Key 哈希化迁移结果。
- 对生产账号凭证执行加密 dry-run 和真实 backfill。
- backfill 后重启 backend 容器并验证健康状态。

## 生产运行态

- 完成后的 backend 容器镜像：`hfc/sub2api:kiro-direct-441c51ca52ed-20260511-151454`
- 容器健康状态：`healthy`
- 内网健康检查：`{"status":"ok"}`
- 公网健康检查：`{"status":"ok"}`

说明：本任务执行过程中，生产机上另一个构建/部署流程把 backend 镜像推进到了 `441c51ca`。该提交已经包含 API Key 哈希化与账号凭证加密提交 `2c125d47`。本任务另外在本机构建并上传了 `linux/amd64` 镜像 `hfc/sub2api:security-keyhash-94fc9580-20260511-153800`，用于运行一次性 backfill 工具容器。

## 备份

- 初始上线前备份：`/opt/relay/backups/security-keyhash-20260511-142604`
- 真实 backfill 前 DB dump：`/opt/relay/backups/security-keyhash-backfill-20260511-154549/sub2api-pre-credential-backfill.dump`
- backfill 前 DB dump sha256 校验：`OK`

## 结果

- API Key 哈希化字段已存在：
  - `api_keys.key_hash`
  - `api_keys.key_prefix`
  - 来自迁移 `138_add_api_key_hash_indexes_notx.sql` 的部分唯一索引
- 有效 API Key 的 `key_hash` 缺失数：`0/88`
- backfill 前账号凭证加密 envelope 数：`0/14`
- 账号凭证 dry-run：`scanned=14 needs_encryption=13 updated=0`
- 账号凭证真实 backfill：`scanned=14 needs_encryption=13 updated=13`
- backfill 后账号凭证加密 envelope 数：`14/14`
- backfill 后敏感凭证字段明文字符串匹配数：`0/14`
- backfill 后二次 dry-run：`scanned=14 needs_encryption=0 updated=0`

## 工具修复

一次性命令 `cmd/encrypt-account-credentials` 已补充 `ent/runtime` blank import，与 server 入口保持一致。没有这个 import 时，Ent 运行时默认值和 validator 不会初始化，工具会在 security secret bootstrap 阶段 panic，尚未进入账号扫描。

## 清理

- 生产机临时 Go 编译镜像和 Go cache 已清理。
- 生产机磁盘使用率从 `92%` 降到 `89%`。
- backfill 容器运行时临时生成的 env 文件已在每次运行后删除。

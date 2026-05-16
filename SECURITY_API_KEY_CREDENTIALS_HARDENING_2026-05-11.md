# API Key 哈希化与上游凭证加密落库改造记录

日期：2026-05-11

## 处理结论

本次已完成可兼容上线的安全改造：

1. API Key 新增 `key_hash` 与 `key_prefix` 字段。
2. API Key 认证查询改为优先按 SHA-256 哈希匹配，并保留旧明文字段兼容。
3. 新建、删除 API Key 时同步维护哈希值与非敏感前缀。
4. 账号 `credentials` 写入数据库前会对敏感字段加密；读取时由仓储层解密给业务层使用。
5. 新增既有账号凭证加密 backfill 命令，默认 dry-run，便于生产环境先扫描再重写。

## API Key 存储变化

新增迁移：

- `backend/migrations/136_add_api_key_hash_columns.sql`
- `backend/migrations/137_backfill_api_key_hash.sql`
- `backend/migrations/138_add_api_key_hash_indexes_notx.sql`

上线后迁移会为既有 `api_keys.key` 回填：

- `key_hash = sha256(key)`
- `key_prefix = left(key, 12)`

认证路径现在使用 `key_hash` 查询，并保留 `key` 查询作为兼容兜底。旧 `key` 明文字段本阶段仍保留，避免影响依赖完整 key 的旧管理脚本、列表和删除逻辑；后续可在确认无依赖后进入“明文字段清空/移除”阶段。

## 上游账号凭证加密

加密入口在仓储层，覆盖：

- 账号创建
- 账号更新
- 单账号凭证更新
- 批量凭证更新

敏感字段会被封装为 AES-256-GCM 加密 envelope，非敏感配置如 `base_url`、`model_mapping` 保持可读，避免破坏路由配置。

当前敏感字段集合包括：

- `api_key`
- `access_token`
- `refresh_token`
- `id_token`
- `session_key`
- `password`
- `client_secret`
- `private_key`
- `service_account_json`
- `token`
- `auth_token`
- `bearer_token`
- `cookie`
- `cookies`
- `secret`
- `secret_access_key`
- `credentials`

## 既有凭证处理命令

默认只扫描，不改库：

```bash
go run ./cmd/encrypt-account-credentials --dry-run=true
```

确认扫描结果后再执行重写：

```bash
go run ./cmd/encrypt-account-credentials --dry-run=false
```

该命令只输出扫描数量、需要加密的账号数量和实际更新数量，不打印任何凭证明文。

## 验证记录

本机 Codex shell 没有本地 `go`，因此使用项目 Docker Go 镜像执行验证：

```bash
docker run --rm --user $(id -u):$(id -g) -e GOCACHE=/gocache -e GOMODCACHE=/gomod -v /tmp/sub2api-gomod:/gomod -v /tmp/sub2api-gocache:/gocache -v "$PWD":/app -w /app/backend golang:1.26.2-alpine go test ./...
```

结果：通过。

## 后续收尾建议

1. 生产部署后先观察 API Key 哈希认证路径和账号凭证解密路径。
2. 执行 `cmd/encrypt-account-credentials` dry-run，确认需要加密的账号数量。
3. 在维护窗口执行 `--dry-run=false`，完成既有上游凭证重写。
4. backfill 后滚动重启服务或刷新调度缓存，确保所有进程重新从数据库读取解密后的凭证。
5. 确认所有业务路径不再依赖 `api_keys.key` 明文后，再规划清空/移除旧明文字段。

# Cursor 账号添加接口与操作说明

## 当前接口

管理端新增接口：

```http
POST /api/v1/admin/accounts/cursor-sidecar
```

它会先把 Cursor 上游凭据写入 `cursor-sidecar`，再在 Sub2API 主库里创建 `platform=cursor,type=upstream` 账号。主库只保存：

```json
{"sidecar_account_ref":"cursor-account@example.com"}
```

`access_token`、`refresh_token` 不会写入 Sub2API 主库，也不会在接口响应里回显。

## 必备材料

- Cursor 账号邮箱：建议与 Cursor 本地登录邮箱一致。
- Cursor `access_token`：短期访问令牌。
- Cursor `refresh_token`：长期刷新令牌；没有这个就不能稳定运行。
- `cursor_service_machine_id`：强烈建议提供；Cursor 上游校验会用到机器标识。
- Cursor token 过期时间：可不填；如果 `access_token` 是可解析 JWT，sidecar 会自动从 `exp` 里读取。
- `cursor_client_version`：当前生产默认建议 `3.3.30`。
- `cursor_config_version`、`cursor_client_id`、`cursor_membership_type`：有就填；没有可留空。
- Sub2API 分组：选择 `cursor-default`，不要选普通 Anthropic/OpenAI 分组。

## 从本机 Cursor 取材料

macOS 默认位置：

```bash
~/Library/Application Support/Cursor/User/globalStorage/state.vscdb
```

需要读取的 key：

```text
cursorAuth/accessToken
cursorAuth/refreshToken
cursorAuth/cachedEmail
cursorAuth/stripeMembershipType
storage.serviceMachineId
cursorAuth/clientId
cursorAuth/clientVersion
cursorAuth/configVersion
```

可用只读 SQL 查看：

```bash
sqlite3 -json "$HOME/Library/Application Support/Cursor/User/globalStorage/state.vscdb" \
  "SELECT key,value FROM ItemTable WHERE key IN (
    'cursorAuth/accessToken',
    'cursorAuth/refreshToken',
    'cursorAuth/cachedEmail',
    'cursorAuth/stripeMembershipType',
    'storage.serviceMachineId',
    'cursorAuth/clientId',
    'cursorAuth/clientVersion',
    'cursorAuth/configVersion'
  );"
```

如果 Cursor 版本把数据放到 `cursorDiskKV`，把 SQL 里的 `ItemTable` 换成 `cursorDiskKV` 再查一次。

## 后台操作方式

1. 打开管理后台账号管理。
2. 点击创建账号。
3. Platform 选择 `Cursor`。
4. 填写账号名称，例如 `cursor-prod-002`。
5. 选择分组 `cursor-default`。
6. 填入 Cursor Email、Access Token、Refresh Token。
7. 填入 `Service Machine ID`，`Client Version` 建议保持 `3.3.30`。
8. 其他字段按需填写，点击创建。

成功后后台会新增一个 Cursor 账号；下游 key 绑定 `cursor-default` 分组后，调用：

```text
/cursor/v1/models
/cursor/v1/chat/completions
/cursor/v1/messages
/cursor/v1/responses
```

## API 示例

```json
{
  "name": "cursor-prod-002",
  "email": "cursor-account@example.com",
  "access_token": "信息缺失",
  "refresh_token": "信息缺失",
  "cursor_token_expires_at": "2026-05-15T12:00:00Z",
  "cursor_service_machine_id": "信息缺失",
  "cursor_client_version": "3.3.30",
  "cursor_config_version": "信息缺失",
  "cursor_client_id": "信息缺失",
  "cursor_membership_type": "pro",
  "group_ids": [21],
  "concurrency": 1,
  "priority": 1,
  "rate_multiplier": 1
}
```

## 安全注意

- 不要把 Cursor token 发到聊天、工单或 Git。
- 添加前确认这是可用于中转的上游账号，避免把个人主力 Cursor 登录态混入生产池。
- 添加后先用灰度 key 走 `/cursor/v1` 小请求验证，再扩大使用。
- Cursor 上游仍是非公开逆向接口，版本漂移或账号风控时，优先禁用对应账号而不是自动混入普通 `/v1`。

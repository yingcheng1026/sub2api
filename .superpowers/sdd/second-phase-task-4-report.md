# 第二阶段 Task 4：继承测试失败修复

## 结论

- 仅修复陈旧 fixture、mock 和测试预期；未修改前端产品代码或 API 合约。
- 前端完整 Vitest：102 个文件、596 个测试全部通过，0 unhandled error。
- backend page path 测试兼容 macOS `/var` 到 `/private/var` realpath；handler 包级测试已通过。
- 生产保持 `NO-GO`；未部署、未重启、未改生产数据。
- 私有 503 原始记录未读取、未修改、未暂存、未提交。

## 修复项

1. `EmailVerifyView`：把 `AFF123` 放入当前 pending OAuth 建号场景的 `register_data`，继续明确验证返利邀请码透传；请求断言改用 `objectContaining` 兼容现行新增可选字段，但未弱化核心字段。
2. `ModelDistributionChart` / `GroupDistributionChart`：fixture 补齐现行必填 `account_cost`。
3. auth source defaults：fixture 和 payload 断言补齐 GitHub、Google。
4. `usePersistedPageSize`：按现行产品行为优先有效用户持久化值；新增非法值回退系统默认测试。
5. `WalletBalanceCard`：改为 `vue-i18n` partial mock，保留 `createI18n` 等真实导出。
6. `DashboardView`：fixture 补齐 `today_account_cost`、`total_account_cost`，消除异步重渲染 unhandled rejection。
7. `page_handler_test.go`：期望路径通过 `filepath.EvalSymlinks` 解析，与被测函数的 realpath 行为一致。

## 验证证据

```text
./node_modules/.bin/vitest run src/views/auth/__tests__/EmailVerifyView.spec.ts --reporter=dot
Test Files 1 passed (1)
Tests 7 passed (7)

./node_modules/.bin/vitest run --reporter=dot
Test Files 102 passed (102)
Tests 596 passed (596)
Duration 11.35s

./node_modules/.bin/vue-tsc --noEmit
exit 0
```

完整套件只有既有、预期的 stderr 场景日志和 Vue `router-link` 测试警告；无失败、无未处理 Promise rejection。

## 独立审查

- 代码审查：`PASS`。
- 首轮审查发现返利邀请码断言被弱化；已改为在目标 fixture 中显式提供并断言 `AFF123`，复审确认无阻断项。

# 第二阶段安全依赖与工具链收口

## 结论

- 前端生产依赖审计从 `4 high` 降为 `0 high / 0 critical`。
- 后端 `govulncheck` 从 5 个可达漏洞降为 0。
- Go 构建、CI、发布和安全扫描工具链统一到 `1.26.5`。
- Go 1.26.5 下 unit、integration、vet 和全量 race 均通过。
- 前端 596 个测试、ESLint、类型检查、生产构建和 XLSX Blob 导出 smoke 均通过。
- 未部署、未重启、未修改生产数据；私有 503 原始记录未读取、未修改、未暂存。

## 前端依赖

### 修复前

`pnpm audit --prod --audit-level=high`：

- `xlsx`：2 个 high，原例外已过期。
- `js-cookie`：1 个 high，无例外。
- `form-data`：1 个 high，无例外。

### 修复

- SheetJS 0.20.3 官方 tarball 已 vendoring 到 `frontend/vendor/xlsx-0.20.3.tgz`，依赖使用 `file:vendor/xlsx-0.20.3.tgz`；`.gitignore` 精确放行该文件，保证干净检出和 Docker build context 都能取得依赖。
- pnpm lock 固定该 tarball 的 SHA-512 integrity；本地重新计算与 lock 值一致。
- pnpm override：`js-cookie=3.0.8`。
- pnpm override：`form-data=4.0.6`。
- 使用仓库 Dockerfile 同版本 `pnpm 9.15.4` 重建 lockfile，并用 frozen lockfile 安装验证。
- 根 Dockerfile、部署 Dockerfile、三条 CI workflow 和 `packageManager` 全部精确固定 `pnpm 9.15.4`。

### 修复后

```text
critical: 0
high: 0
Audit exceptions validated.
```

XLSX 写出 smoke：`ArrayBuffer` 15952 bytes，经现有 `Blob([out])` 路径生成同尺寸 Blob，PASS。

说明：vendoring 后 audit 原始进程仍因 low/moderate 项返回 1；项目 CI 按既有规则只以 high/critical 异常门禁判定，当前门禁为 `0 high / 0 critical` 且 exceptions checker PASS。未隐藏 low/moderate 计数。

## 后端依赖

### 修复前

`govulncheck ./...` 检出 5 个可达漏洞：

- Go 1.26.3 标准库：3 个。
- AWS S3/EventStream：1 个。
- `golang.org/x/net`：1 个。

### 修复

- Go：`1.26.5`。
- AWS S3：`v1.97.3`。
- AWS EventStream：`v1.7.8`。
- `golang.org/x/net`：`v0.55.0`。
- `go get` 引入的最小联动模块升级由 `go mod tidy` 固定到 `go.mod/go.sum`。

### 修复后

```text
No vulnerabilities found.
Your code is affected by 0 vulnerabilities.
go mod verify: all modules verified
```

## 回归证据

- Go 1.26.5 unit：全包 PASS；`internal/service` 85.242s。
- Go 1.26.5 integration：全包 PASS；repository 6.762s，service 45.392s。
- Go 1.26.5 vet：PASS。
- Go 1.26.5 race：全包 PASS。
- Frontend Vitest：102 files / 596 tests PASS。
- Frontend ESLint、`vue-tsc --noEmit`、Vite build：PASS。
- Claude Code 本地 Messages identity harness：12/12 PASS。
- deploy retention / Docker build guard：PASS。

## Secret 扫描边界

- gitleaks 全历史真实扫描：4379 commits、约 63.07 MB，发现 106 个 inherited 历史记录。
- 本轮不改写 Git 历史；该项保留为生产 `NO-GO` 风险输入。
- 最终提交后必须单独扫描 `931915e3..HEAD`，证明本轮没有新增泄漏。
- 仓库 `make secret-scan` 引用的 `tools/secret_scan.py` 已被历史提交删除，因此该 Make target 仍为 `信息缺失`，不得虚报通过。

## 生产边界

- 本轮只开发、测试和构建本地制品。
- 不运行、不推送最终镜像。
- 不部署、不重启、不改生产配置或生产数据。

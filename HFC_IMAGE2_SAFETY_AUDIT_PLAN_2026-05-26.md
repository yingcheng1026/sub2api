# HandsFreeClub Image 2 生图安全审计层完整版方案

日期：2026-05-26  
适用范围：HandsFreeClub / Sub2API 中转层对 OpenAI `gpt-image-2` 生图、修图请求的重新开放方案  
目标：在重新开放大客户生图流量前，把中转层安全审计、运营处置、日志追责、上线验收标准补齐，降低上游封号、违法牵连和批量滥用风险。

## 1. 结论

可以重新开放，但不能只靠“Prompt 审计”上线。

建议采用以下上线边界：

```text
白名单客户 -> 组级生图开关 -> 请求参数闸门 -> 输入审计 -> 上游强制标准 moderation -> 输出审计 -> 审计日志/风险分 -> 自动限权/封禁
```

MVP 上线必须具备：

1. 生图只对白名单分组开放，不默认全站开放。
2. 输入 Prompt 和编辑参考图必须同步审计，命中风险直接拦截。
3. 禁止客户使用 `moderation=low`，中转层必须强制 `auto` 或拒绝。
4. 初期禁止流式生图和 partial images，避免未审计图片先发给客户。
5. 非流式输出必须先在中转层缓冲，二次审计通过后再返回。
6. 所有请求绑定用户、API key、分组、上游 request id、审计结果和风险处置。
7. 对严重违规和重复违规用户自动限权或封禁。

如果输出审计暂时没有实现，建议不要直接全量开放，只能做“极小白名单 + 强输入审计 + 强上游 `moderation=auto` + 禁止流式 + 强日志”的灰度。

## 2. 官方安全口径

当前 OpenAI 图像文档显示，`gpt-image-2` 属于 GPT Image 模型，图像 API 支持 `generations` 与 `edits` 两类核心端点。OpenAI 文档也明确：prompts 和 generated images 会按内容政策过滤；GPT Image 模型支持 `moderation` 参数，其中 `auto` 是默认标准过滤，`low` 更宽松。

OpenAI 安全最佳实践建议使用 Moderation API、红队测试、人工复核、KYC、限制输入/输出范围、问题上报入口和 `safety_identifier`。Moderation API 支持文本与图片输入，并覆盖 harassment、hate、illicit、self-harm、sexual、sexual/minors、violence、violence/graphic 等类别。

OpenAI Usage Policies 明确禁止或限制的高风险方向包括：非自愿亲密内容、性暴力、恐怖主义或暴力、武器开发/采购/使用、违法活动、规避安全措施、未经同意使用真人肖像造成真实性混淆，以及任何剥削、危害或性化未成年人的内容。

参考：

- https://developers.openai.com/api/docs/guides/image-generation
- https://developers.openai.com/api/docs/guides/safety-best-practices
- https://developers.openai.com/api/docs/guides/moderation
- https://openai.com/policies/usage-policies/

## 3. 当前代码基线

本轮只读查看本地 `sub2api-fork-codex`，未检查实时生产配置。生产实际开关、DB 行和当前运行镜像状态仍需上线前重新核对。

已具备的基础能力：

1. 图像路由已经独立出来：
   - `POST /v1/images/generations`
   - `POST /v1/images/edits`
   - 相关实现：`backend/internal/handler/openai_images.go`
2. 默认图像模型为 `gpt-image-2`：
   - 相关实现：`backend/internal/service/openai_images.go`
3. 模型边界已限制为 `gpt-image-*`：
   - 非图像模型请求会被拒绝。
4. 分组级生图开关已存在：
   - `groups.allow_image_generation`
   - `GroupAllowsImageGeneration(apiKey.Group)`
   - 当前图像 handler 顺序是先查组级生图权限，再做内容审计。
5. 风控中心内容审计已存在：
   - 全局开关：`risk_control_enabled`
   - 配置：`content_moderation_config`
   - 模式：`off`、`observe`、`pre_block`
   - 默认模型：`omni-moderation-latest`
   - 审计日志表：`content_moderation_logs`
   - 支持命中日志、非命中日志、阈值快照、输入摘要、自动封禁、邮件提醒、hash 预拦截、过期清理。
6. 图像输入审计已经接入：
   - `OpenAIImagesRequest.ModerationBody()` 会把 `prompt` 和 `images` 组装给内容审计。
   - multipart 上传的参考图、mask 图会转为 data URL 进入审计。

当前关键缺口：

1. 输出后审计缺失或未形成强制闭环：当前非流式响应在 `handleOpenAIImagesNonStreamingResponse` 内直接写回客户端；流式响应会更早发送 partial/final image event。
2. `moderation=low` 目前会被解析并转发，未看到强制拒绝或覆盖为 `auto` 的中转层策略。
3. `safety_identifier` 未看到在 Images API 请求中被统一注入；OpenAI 推荐用稳定、隐私保护的用户标识帮助定位滥用。
4. 当前 moderation 结果结构主要消费 `category_scores`，未充分利用官方返回里的 `categories` 和 `category_applied_input_types`。
5. 初始阈值是通用内容审计阈值，不一定适合生图业务的更高风险。
6. 对真人肖像、非自愿亲密图、未成年人年龄不确定、名人/公众人物仿冒等图像专属风险，还需要本地业务规则补强。

## 4. 风险模型

### 4.1 上游账号风险

风险：用户通过你们的中转账号池提交违规生图请求，上游将风险归因到你们组织或账号池，导致模型访问受限、账号封禁或整组 API 能力受影响。

控制目标：把违规用户和正常用户隔离；能向上游和内部审计证明你们有前置审查、用户追踪和处置机制。

### 4.2 法律和平台责任风险

风险：用户生成色情、血腥、暴力、非自愿亲密图、侵犯肖像权/隐私权/知识产权内容，并用于传播或违法行为，平台被认定没有尽到合理审核义务。

控制目标：明确用户协议、保留必要证据、建立违规处置记录、对严重风险立即封禁。

### 4.3 技术绕过风险

风险：用户用多语言、错别字、谐音、分段、prompt injection、图像编辑参考图、`moderation=low`、流式输出等方式绕过审计。

控制目标：不只审一个原始 Prompt，而是审计完整请求意图、参考图、关键参数和生成结果。

### 4.4 成本和可用性风险

风险：生图延迟长、成本高，恶意客户用大尺寸、高质量、多张并发消耗账号池和额度。

控制目标：初期限制 `n`、尺寸、质量、并发、频率、每日额度，并对高风险用户降权或停权。

## 5. 目标请求链路

```text
Client
  |
  v
API Key / User Auth
  |
  v
GroupAllowsImageGeneration
  |
  v
Image Request Guard
  - endpoint allowlist
  - model allowlist
  - n / size / quality / stream / partial_images limit
  - reject or overwrite moderation=low
  |
  v
Input Safety Audit
  - prompt text
  - edit source images
  - mask/reference images
  - local policy classifier
  - OpenAI Moderation API
  |
  v
Upstream Call
  - moderation=auto
  - safety_identifier=user_hash
  - request id binding
  |
  v
Output Hold
  - buffer generated image
  - extract b64/url
  |
  v
Output Safety Audit
  - generated image moderation
  - output hash
  - category scores
  |
  +--> pass: return image to client
  |
  +--> fail: block response, log incident, apply risk action
```

## 6. 违规风险控制原则

这套安全审计层的核心目标不是“看起来做了审核”，而是围绕两类顾虑建立可验证闭环：

1. 防止客户通过你们的中转链路向上游提交违规请求，导致上游账号、组织或模型能力被限制。
2. 防止客户成功拿到违规图片并外传，让平台在法律、监管、投诉或上游追责里变成无防护的服务提供者。

因此策略必须满足四个原则：

1. **违规前阻断**：高风险 Prompt、参考图、参数组合在请求上游前被挡住。
2. **违规后不出图**：即使上游生成了不合规输出，中转层也不能把图片交给客户。
3. **违规者可追踪**：每一次命中能定位到 user、API key、group、request id、上游 request id 和审计结果。
4. **重复违规可处置**：普通误触、反复测试边界、严重违法风险要有不同处罚动作。

为了满足这四点，生图安全层必须采用 fail closed：只要审计服务不可用、输出无法解析、输出无法完成审计、客户试图降低审计强度，就拒绝生图请求，而不是放行。

### 6.1 违规风险分级

| 等级 | 内容类型 | 动作 | 说明 |
| --- | --- | --- | --- |
| S0 极严重 | 未成年人性化、CSAM、诱导未成年人、非自愿亲密图、报复性色情、性暴力、恐怖主义宣传、明确违法武器/爆炸物、诈骗/证件伪造/违法服务 | 立即拒绝；冻结生图权限；人工复核是否封号；保留证据摘要 | 这类风险直接对应上游封号和法律牵连 |
| S1 严重 | 露骨色情、真人/名人色情化、血腥肢解、酷刑、极端暴力、仇恨威胁、规避审计/绕过安全策略 | 拒绝；累计违规；短期内多次命中自动停权 | 即使不一定违法，也足以触发上游和平台安全风险 |
| S2 高风险边界 | 真人肖像仿冒、名人/公众人物写实图、年龄不明但有性暗示、偷拍视角、医疗/犯罪现场写实、政治/公共事件误导性图像 | 默认拒绝或进入人工复核；仅授权客户可开白特定模板 | 这类内容容易转化成侵权、隐私或虚假信息风险 |
| S3 普通风险 | 轻微暴力、非写实战斗、成人但非露骨、夸张恐怖风格、敏感广告素材 | 根据阈值和业务模板处理；保守起步 | 灰度期宁可误杀一些，后续靠样本校准 |
| S4 低风险 | 商品图、海报、插画、头像、UI 素材、室内设计、风景、普通角色设定 | 放行并记录最小日志 | 主要业务收入应引导到这些安全场景 |

### 6.2 硬拒绝规则

以下情况不进入上游请求：

1. Prompt 或参考图命中 S0。
2. Prompt 明确要求绕过审核、规避政策、降低安全过滤。
3. 客户显式传入 `moderation=low`。
4. 客户请求 `stream=true` 或 `partial_images`，但输出审计无法 hold-and-release。
5. 编辑接口传入高风险参考图，即使 Prompt 本身安全。
6. 请求缺少可追踪用户身份，或 API key 不属于允许生图的白名单分组。
7. moderation 服务不可用、超时、无可用审核 API key，或审核结果结构异常。

### 6.3 输出拦截规则

以下情况即使上游已经生成，也不得把图片返回给客户：

1. 输出图命中 sexual/minors、sexual、violence/graphic、self-harm、illicit/violent 等高风险分类。
2. 输出图被本地图像规则判断为真人色情化、未成年人风险、非自愿亲密图、血腥写实或明显违法用途。
3. 输出图片无法下载、无法解析、MIME 不可信、超过大小限制或审计失败。
4. 流式或 partial 图片无法在发送前完成审计。

输出被拦截时，客户只能收到通用错误：

```json
{
  "error": {
    "type": "invalid_request_error",
    "code": "content_policy_violation",
    "message": "内容审计命中风险规则，请调整输入后重试"
  }
}
```

内部日志保留完整原因，外部错误不暴露具体绕过线索。

### 6.4 上游封号风险隔离

为减少一个客户拖垮整个上游账号池：

1. 生图客户使用独立 group，不和普通聊天客户混用。
2. 生图上游账号池与聊天账号池尽量隔离。
3. 高风险客户使用更低额度、更低并发、更严格阈值。
4. 每个上游请求带脱敏 `safety_identifier`，让上游能定位终端用户而不是只看到你们组织。
5. 同一用户连续触发违规时，先停该用户/该 API key 的生图能力，不影响其他客户。
6. 一旦某个客户触发 S0，立即从全部生图分组移除，人工复核后再决定是否恢复。

### 6.5 法律牵连风险降低

这套系统不能保证“完全免责”，但能证明平台尽到了合理审核和处置义务。

必须形成以下证据链：

1. 客户开通时同意生图使用条款。
2. 请求进入中转层时做了输入审计。
3. 输出返回前做了结果审计。
4. 命中违规时平台没有返回图片。
5. 重复违规时平台执行了限权/封禁。
6. 严重违规时平台保留了 hash、摘要、分类、时间、用户和 request id。
7. 平台提供投诉/举报和人工复核入口。

证据链只保留必要信息，不长期保存大量原始敏感图像，避免平台自己变成违规内容仓库。

## 7. 安全审计层设计

### 7.1 第一层：资格准入

生图能力不要做成“所有 GPT 分组默认打开”。

建议：

1. 新建或复用专门的 Image 白名单分组。
2. `allow_image_generation=false` 作为普通分组默认值。
3. 大客户开通前至少满足：
   - 已登录/实名或企业联系人可追溯。
   - 已付费或签约。
   - 明确业务场景。
   - 接受生图使用条款。
4. 高风险行业客户进入人工审核：
   - 成人内容、陪聊/擦边营销、博彩、虚拟币诈骗、假证件、灰产广告、真人仿冒、批量社媒素材等。

信息缺失：当前这批大客户的行业、使用场景、日流量、是否企业主体、是否有合同和违规赔付条款还未提供。

### 7.2 第二层：请求参数闸门

初期建议参数策略：

| 参数 | 初期策略 | 原因 |
| --- | --- | --- |
| `model` | 只允许 `gpt-image-2` 或明确允许的 `gpt-image-*` | 防止混入非预期模型 |
| `endpoint` | 只允许 `/v1/images/generations`、`/v1/images/edits` | 缩小攻击面 |
| `n` | 初期强制 `1` | 降低单次违规扩散和成本 |
| `size` | 先限制在官方常用尺寸或业务定价尺寸 | 控成本和延迟 |
| `quality` | 默认 `medium`，高质量只对白名单开放 | 控成本 |
| `stream` | 初期拒绝 `true` | 输出审计前不能边生成边返回 |
| `partial_images` | 初期拒绝或强制 `0` | partial 图片未经输出审计 |
| `moderation` | 拒绝 `low`，缺省或强制 `auto` | 避免客户主动降安全级别 |
| `response_format` | 优先 `b64_json` 或内部可审计格式 | 便于输出审核 |
| `images` | 限数量、限大小、限 MIME | 降低编辑图绕过和成本 |

建议错误返回：

```json
{
  "error": {
    "type": "invalid_request_error",
    "code": "image_safety_guardrail",
    "message": "当前生图服务不支持该参数，请调整后重试"
  }
}
```

不要把具体命中词、阈值、策略细节完整返回给客户，避免帮助其绕过。

### 7.3 第三层：输入审计

输入审计范围：

1. `prompt`
2. `images[].image_url`
3. multipart `image`
4. `mask`
5. Responses API 里 `image_generation` tool 的 `input`
6. 任何可隐含生图意图的 `tools` / `tool_choice`

已可复用能力：

1. `ExtractContentModerationInput` 已支持 `ContentModerationProtocolOpenAIImages`。
2. `ModerationBody()` 已将 prompt 和编辑图输入抽出。
3. `ContentModerationService` 支持同步 pre-block。

需要补强：

1. 使用 `omni-moderation-latest` 返回的 `categories` 作为硬拦截依据，不只看 `category_scores`。
2. 记录 `category_applied_input_types`，区分是文本命中还是图片命中。
3. 增加图像业务规则：
   - 未成年人性化：零容忍；年龄不确定且带性暗示时拒绝。
   - 非自愿亲密图、偷拍、泄露、报复性色情：拒绝。
   - 真人/名人肖像仿冒、深度伪造、误导真实性：拒绝或要求证明授权。
   - 血腥、肢解、酷刑、极端暴力：拒绝。
   - 武器制造、违法活动、诈骗素材：拒绝。
   - 规避审计、要求绕过安全策略：拒绝。
4. 对中文、英文、中英混写、谐音、拼音、分隔符、emoji 伪装做红队测试。

建议模式：

```text
pre_block
sample_rate=100
record_non_hits=false
record_hits=true
pre_hash_check_enabled=true
```

### 7.4 第四层：输出审计

这是重新开放生图最关键的补强点。

原因：即使 Prompt 看起来安全，模型仍可能生成擦边、暴力、真人误导或其他不适合分发的图片。只审 Prompt 无法覆盖输出偏差。

非流式方案：

1. 上游返回后，中转层先不要立即 `c.Data()` 给客户端。
2. 解析返回体里的图片：
   - `data[].b64_json`
   - data URL
   - OpenAI 返回的 image URL
   - Responses API `image_generation_call.result`
3. 对每张生成图调用 moderation：
   - data URL 可直接作为 `image_url.url`
   - URL 输出需要服务端下载后转 data URL，或仅允许可信上游域名 URL。
4. 审计通过：返回原响应。
5. 审计失败：返回 `content_policy_violation`，不返回图片内容。
6. 记录输出 hash、最高风险类别、分数、用户、分组、API key、上游 request id。

流式方案：

1. 初期不开放 `stream=true`。
2. 初期不开放 `partial_images`。
3. 后续如果要开放，必须改成服务端 hold-and-release：
   - 先缓冲 partial/final 图片。
   - 只把进度事件返回给客户端。
   - 图片内容在输出审计通过后再释放。

### 7.5 第五层：上游调用保护

上游请求建议：

1. 强制 `moderation=auto`。
2. 注入 `safety_identifier`：
   - 值为 `hfc_user_{sha256(user_id + salt)}` 或 `hfc_key_{sha256(api_key_id + salt)}`。
   - 不传明文手机号、邮箱、微信、真实姓名。
3. 绑定 request id：
   - 本地 request id
   - 上游 `x-request-id`
   - 用户 ID
   - API key ID
   - group ID
4. 禁止客户透传可能影响审计策略的字段。

### 7.6 第六层：审计日志

已有表 `content_moderation_logs` 可以继续用，但建议扩展或新增 image 专用字段。

建议记录：

| 字段 | 建议 |
| --- | --- |
| `request_id` | 本地请求 ID |
| `upstream_request_id` | OpenAI 返回的 request id |
| `user_id` | 必填 |
| `api_key_id` | 必填 |
| `group_id` | 必填 |
| `endpoint` | generations / edits |
| `model` | `gpt-image-2` |
| `input_hash` | normalized prompt + input images hash |
| `output_hashes` | 生成图 hash，不保存原图 |
| `input_excerpt` | 脱敏摘要，长度限制 |
| `stage` | input / output / hash_precheck |
| `action` | allow / block / observe / error |
| `highest_category` | 最高风险类别 |
| `category_scores` | 完整分数 |
| `category_flags` | 官方 categories |
| `applied_input_types` | text / image |
| `policy_rule` | 本地业务规则命中 |
| `auto_banned` | 是否自动封禁 |

留存建议：

1. 非命中日志：最多 3 天。
2. 命中日志：90-180 天。
3. 严重违规：保留 hash、摘要和处置记录；除非必要，不长期保存原图。
4. 如必须保存严重违规图片作为证据，需要单独加密存储、访问审计和删除机制。

### 7.7 第七层：处罚和熔断

建议初期比当前通用阈值更严格。

| 场景 | 动作 |
| --- | --- |
| 单次普通命中 | 拦截请求，记录风险分，返回安全提示 |
| 24 小时内 3 次命中 | 自动暂停该 API key 的生图能力 24 小时 |
| 7 天内 5 次命中 | 自动关闭用户生图权限，进入人工复核 |
| 未成年人性化 / CSAM / 非自愿亲密内容 / 恐怖主义 / 明确违法 | 立即封禁账号或 API key，保留证据摘要 |
| 大客户误杀申诉 | 人工复核，必要时调阈值或加白业务模板 |
| moderation 服务不可用 | fail closed：生图请求拒绝，不放行 |

注意：文本聊天可以继续可用，但生图能力应单独冻结，避免一个客户违规影响整个服务。

## 8. 推荐配置

### 8.1 风控中心

```json
{
  "risk_control_enabled": true,
  "content_moderation_config": {
    "enabled": true,
    "mode": "pre_block",
    "base_url": "https://api.openai.com",
    "model": "omni-moderation-latest",
    "timeout_ms": 5000,
    "sample_rate": 100,
    "all_groups": false,
    "group_ids": ["只填允许生图的大客户分组"],
    "record_non_hits": false,
    "pre_hash_check_enabled": true,
    "email_on_hit": true,
    "auto_ban_enabled": true,
    "hit_retention_days": 180,
    "non_hit_retention_days": 3
  }
}
```

### 8.2 初始阈值

当前默认阈值可以作为基线，但图像业务建议做更严格灰度。

建议：

1. `sexual/minors`、非自愿亲密、CSAM：只要官方 categories 命中或本地规则命中，直接拦截。
2. `sexual`：对普通客户严格拦截；如确有合规业务场景，必须人工开白且不能涉及真人/未成年人/非自愿。
3. `violence/graphic`：严格拦截。
4. `violence`：允许非写实、非血腥、非煽动性的低风险场景，但需要阈值和样本校准。
5. `illicit`、`illicit/violent`：严格拦截。
6. `hate/threatening`、`harassment/threatening`：严格拦截。
7. 所有“绕过安全审核”的 Prompt：本地规则直接拦截。

阈值不要一次写死成永久规则。官方 moderation 模型会升级，`category_scores` 需要周期性校准。

## 9. 研发改造清单

### P0：上线前必须完成

1. 参数防护：
   - 拒绝或覆盖 `moderation=low`。
   - 拒绝 `stream=true`。
   - 拒绝 `partial_images`。
   - 初期限制 `n=1`。
2. 输出审计：
   - 非流式图像响应先缓冲。
   - 提取生成图。
   - 调用 moderation。
   - 通过后返回，失败时拦截。
3. 审计结果结构增强：
   - 解析 `categories`。
   - 解析 `category_applied_input_types`。
   - 日志记录 input/output stage。
4. 上游安全标识：
   - Images API / Responses image_generation 都注入 `safety_identifier`。
5. 生图专用处罚：
   - API key 级 image 暂停。
   - 用户级 image 暂停。
   - 严重违规立即禁用。
6. 测试：
   - 参数拒绝测试。
   - 输入命中阻断测试。
   - 输出命中阻断测试。
   - moderation 服务失败时 fail closed 测试。
   - 安全请求通过测试。

### P1：灰度后增强

1. 管理端增加 image 专用审计视图：
   - 输入命中率
   - 输出命中率
   - 分组命中排行
   - API key 命中排行
   - 上游 request id 检索
2. 客户级风控配置：
   - 不同分组不同阈值
   - 不同客户不同额度/质量/尺寸
3. 人工复核队列：
   - 大客户误杀申诉
   - 边界类 Prompt 复核
4. 合规模板：
   - 电商商品图
   - 广告海报
   - UI 素材
   - 插画/头像
   - 禁止真人色情、血腥、仿冒等模板。

### P2：规模化后增强

1. 输出图片水印或元数据标识。
2. 图片 hash 黑名单库。
3. 客户行业风险画像。
4. 异常行为检测：
   - 短时间大量拒绝
   - 多 API key 共享同一高风险 Prompt
   - 多语言混淆
   - 反复测试边界词
5. 每周红队测试和阈值回归。

## 10. 上线流程

### 阶段 0：准备

1. 只读确认生产状态：
   - 当前 running image。
   - `risk_control_enabled`。
   - `content_moderation_config`。
   - 所有 active group 的 `allow_image_generation`。
   - 目标白名单 group。
2. 准备 moderation API key，确认健康。
3. 准备客户使用条款。
4. 准备红队测试集。

### 阶段 1：影子观察

如果现有客户暂未真正生成图片，可以先开 `observe` 跑日志；但如果已经要真实接入生图，输入审计必须直接 `pre_block`。

建议：

1. 对目标白名单 group 开启内容审计。
2. 先不开普通用户分组。
3. 观察 24-48 小时：
   - 命中类别。
   - 误杀样本。
   - moderation latency。
   - 队列堆积。
   - API key 健康。

### 阶段 2：小流量开放

条件：

1. P0 改造完成。
2. 输出审计已上线。
3. 流式和 partial 已禁用。
4. 上游 `moderation=auto` 强制生效。
5. 日志和封禁可查。

开放策略：

1. 只开放 1-3 个白名单客户。
2. 每个客户限流：
   - 每分钟请求数。
   - 每日图片数。
   - 并发数。
   - 最高质量/尺寸。
3. 人工值守首日 2-4 小时。

### 阶段 3：扩大开放

条件：

1. 连续 7 天无严重违规。
2. 输出审计命中处理稳定。
3. 客户误杀率可接受。
4. 上游账号池健康。
5. 账单成本可控。

扩大策略：

1. 分组逐步开，不做全站开关。
2. 每批新增客户后观察 24 小时。
3. 对高风险客户坚持人工审批。

## 11. 运营和法务要求

必须补充用户条款：

1. 用户不得生成、上传、传播违法违规内容。
2. 禁止未成年人性化、CSAM、非自愿亲密图、偷拍、报复性色情。
3. 禁止仿冒真人/名人、侵犯肖像权、隐私权、著作权或商标权。
4. 禁止恐怖主义、极端暴力、武器违法、诈骗、赌博、灰产广告。
5. 用户对生成内容及用途承担责任。
6. 平台有权拦截、记录、暂停、封禁、配合调查。
7. 平台不保证所有违规内容都能被自动检测，用户仍需自行合规。

信息缺失：具体合同、隐私政策、数据保留告知和法务措辞需要律师或合规顾问最终确认。

## 12. 验收标准

上线前必须可验证：

1. 普通未授权分组请求 `/v1/images/generations` 返回 403。
2. 白名单分组安全 Prompt 可以生成。
3. 高风险 Prompt 在上游调用前被阻断。
4. 编辑接口中的高风险参考图被阻断。
5. 客户传 `moderation=low` 被拒绝或覆盖。
6. 客户传 `stream=true` 被拒绝。
7. 客户传 `partial_images` 被拒绝。
8. 生成结果命中风险时不返回图片。
9. moderation API 故障时请求不放行。
10. 审计日志能按 user / api key / group / request id 查到。
11. 连续违规后能自动暂停生图能力。
12. 上游请求带有稳定、脱敏的 safety identifier。

## 13. 建议的产品口径

对客户可以这样表达：

> 生图服务已开放给白名单客户使用。为保护账号池稳定和合规安全，平台会对生图 Prompt、参考图和生成结果进行自动安全审计。涉及未成年人性化、非自愿亲密内容、血腥暴力、违法活动、真人仿冒、侵权或绕过安全策略的请求会被拒绝；多次违规会暂停或关闭生图权限。

不要承诺：

1. “任何内容都能生成”
2. “不审查”
3. “可以绕过官方限制”
4. “不会封号”
5. “出了问题平台完全不承担任何责任”

## 14. 最小上线任务拆分

建议研发按以下顺序做：

1. `image-request-guard`：参数闸门和 `moderation=low` 防护。
2. `image-output-moderation`：非流式输出缓冲审计。
3. `image-safety-identifier`：上游 safety identifier 注入。
4. `moderation-result-schema`：解析 categories / applied input types。
5. `image-abuse-actions`：生图专用限权和封禁。
6. `image-safety-tests`：红队和回归测试。
7. `image-whitelist-rollout`：白名单分组灰度上线。

## 15. 当前判断

你们现在的代码已经有不错的安全底座：分组开关、内容审计、审计日志、自动封禁、hash 预拦截、图像输入抽取都已有雏形。

但如果要重新开放 `gpt-image-2` 给大客流，当前方案还差几个“上线前必补”的硬点：

1. 输出审计。
2. 禁止流式/partial 未审先发。
3. 禁止 `moderation=low`。
4. 上游 `safety_identifier`。
5. 生图专用处罚阈值。
6. 图像专属政策规则和验收测试。

补齐后，可以先对白名单客户灰度开放；没补齐前，不建议全量恢复生图服务。

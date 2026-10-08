# 文本提示词管理：剩余三项收尾报告

日期：2026-10-08
仓库：`G:\My_Project\注册机\sub2api`（分支 `main`，基于 `08f0653eb`）
范围：交接文档 `docs/plans/2026-10-08-handoff.md` 第 3 节的剩余三项（3.1 / 3.2 / 3.3）
状态：三项已完成；四项前端门禁 + `go vet -tags=unit ./...` + 前端全量单测通过。
另在复核后端测试时查出并修掉本功能的一个真缺陷（3.4：功能开关的环境变量被静默忽略）。
**真实环境端到端已补做**：管理端 API（真实 HTTP）+ 三个页面的真实浏览器点击（25 项断言全过），
见 4.1；仍未复现的是注入链路与遥测写入，见 4.2。**改动未提交**，工作树留待复核。

---

## 1. 3.1 请求记录分页

### 后端契约（未改动，仅复核）

`backend/internal/handler/admin/prompt_template_handler.go` 的 `ListRequestEvents`：
只给 `limit` → 返回数组；给了 `page`/`page_size` → 返回
`{items,total,page,page_size,pages}`。`total` 来自真实 `COUNT`，窗口来自
`Pagination.Limit()/Offset()`（`repository/prompt_template_repo.go:730`、`:782`），
所以信封里的 `total` 是可用的总条数，不是当前页长度。

### 前端改动

| 文件 | 改动 |
|---|---|
| `features/prompt-templates/types.ts` | 新增 `PromptRequestEventPageQuery`（`page` + `page_size`，继承筛选条件）；`PromptRequestEventQuery` 保留 `limit` 作为历史契约 |
| `features/prompt-templates/api.ts` | `listRequestEvents` 用**重载**表达两种返回形态：传分页参数 → `PaginatedResponse<PromptRequestEvent>`；其余 → `PromptRequestEvent[]` |
| `features/prompt-templates/PromptRequestEventsView.vue` | `page`/`pageSize` 状态（`pageSize` 取 `getPersistedPageSize()`，与仓库既有分页页一致）、表格下方 `Pagination` 控件、换页/换页大小/重新筛选的三种触发（筛选与换页大小都回到第 1 页） |
| `features/prompt-templates/components/RequestEventFilterBar.vue` | 移除 `条数上限` 下拉与 `limitHint` 文案 |

**为什么移除过滤栏的 limit 控件**：它原本只是「一次取多少条」的替代品。加分页后
若保留，就会出现两个互相竞争的「每页条数」控件；且后端一旦收到 `page/page_size`
就忽略 `limit`，那个控件会变成一个点了没用的死控件。现在每页条数统一由
`Pagination` 控件管理（沿用仓库既有的本地持久化行为）。

### 证据

`frontend/src/features/prompt-templates/__tests__/api.spec.ts`（3 项）断言：只传
`limit` 时请求参数与返回数组不变；传 `page/page_size` 时参数原样下发、信封原样返回；
无参调用退化为无筛选的历史形态。

`__tests__/requestEventsView.spec.ts`（5 项）断言：首屏带 `page=1`；换页只改 `page`
不改 `page_size`；换 `page_size` 回到第 1 页；重新筛选回到第 1 页且保留筛选条件。

---

## 2. 3.2 审计事件列表渲染

新增 `features/prompt-templates/components/TemplateEventPanel.vue`，接入
`PromptTemplateDetailView.vue`（与版本历史面板并列，`load()` 里与草稿/校验/版本并行加载）。

- 列：时间 / 动作 / 作用域 / 操作者 / 状态变化，只读，无写操作。
- 动作与作用域走**固定枚举 → i18n**，未登记的取值回退到 `unknown` 文案并原样显示原始值，绝不臆造。
- 状态摘要：`before_state` / `after_state` 渲染成**键值 chip**（`变更前 … → 变更后 …`），
  不倾倒原始结构。SHA-256 截断显示（完整值放在 `title`），布尔值、空数组、`mode` 枚举都有专门处理。
- 需要看原文时才点「展开原始 JSON」，以 `<pre>` 呈现 `{before_state, after_state}`。
- 面板自身也遵守「错误态不谎报空列表」：`error` 存在时不渲染表格，因此不会同时出现
  错误横幅和「还没有任何管理操作记录」。

覆盖的枚举（来自 `service/prompt_template.go`）：9 个 action、4 个 scope、
9 个状态字段名；状态字段实际取值只有 `name` / `archived` / `mode` / `body_sha256` /
`body_bytes` / `client_models` / `supported_profiles` / `version_no` / `manifest_sha256`。

证据：`__tests__/templateEventPanel.spec.ts`（6 项）覆盖枚举翻译与未知回退、操作者
三级回退（`actor_name` → `actor_id` → 未记录）、键值 chip 而非原始 JSON、哈希截断 +
按需展开才出现完整哈希、`mode` 枚举翻译、错误态不出现「无记录」。

---

## 3. 3.3 三个 UX 问题的定性

### 3.3-1 首次使用引导弹窗（「欢迎使用 Sub2API」，1 of 21）

**性质：设计行为（一次性、可关闭、可重开），不是「反复出现」的缺陷；但发现并修复了其中一处真缺陷。**

- 只在**管理员 + 标准模式**下自动启动，且 `localStorage` 里
  `admin_guide_<userId>_admin_v4_interactive` 不为 `true` 时才会启动
  （`composables/useOnboardingTour.ts` 的 `onMounted`、`AppLayout.vue` 的 `storageKey`）。
  也就是说：**每个管理员用户、每个引导版本只出现一次**，不满足「反复出现」。
- 它是挂在 `AppLayout` 上的全局引导，因此会出现在管理员进入的任意路由——包括深链直达的
  模板详情页。这是全局首启引导的既有设计，路由无关，**未改**。
- 可退出路径：`ESC`（`markAsSeen` + 销毁）、弹窗右上角关闭按钮（`onCloseClick`，同样
  `markAsSeen` + 销毁）、或走完 21 步（最后一步的 Next 即完成）。头部头像菜单可「重新查看新手引导」。
- **发现的真缺陷（含机制更正）**：欢迎步骤把「上一页」按钮复用作「跳过」文案
  （`prevBtnText: '跳过'`）。**driver.js 1.4.0 在第一步会强制注入
  `disableButtons: ['previous']`**（`dist/driver.js.mjs`：`disableButtons: [...h ? [] : ["previous"]]`，
  `h` 是上一步，第一步为 `undefined`），所以这个「跳过」按钮是**渲染出来但被禁用**的：
  点它不会触发任何 hook，而 `allowClose: false` 又禁止点遮罩关闭。也就是说
  「跳过」这个标签**只会在一个不可点击的状态下出现**——交接文档给的判据
  「可通过『跳过』关闭才算设计行为」原本并不成立。
  **处置（两处，缺一不可）**：
  1. `components/Guide/steps.ts`：两个欢迎步骤的 popover 显式加 `disableButtons: []`
     （driver.js 把步骤级 popover 放在最后展开，因此能覆盖掉上面的默认禁用），
     让「跳过」按钮可点击；
  2. `composables/useOnboardingTour.ts`：`onPrevClick` 在第 0 步改为
     `markAsSeen()` + 销毁引导（其余步骤仍是「上一页」）。

  > 更正说明：本报告初稿把原因写成「点了没反应（movePrevious 空转）」，只改了第 2 处。
  > 复核 driver.js 源码后发现按钮其实被 `disabled`，只改 hook 是**无效修复**——
  > 现已补上第 1 处，并用下面的测试锁住两半。

  证据：`composables/__tests__/useOnboardingTour.skip.spec.ts`（3 项）用 mock 的 `driver.js`
  抓取真实传给 `driver()` 的配置：断言两个欢迎步骤 `disableButtons === []` 且带「跳过」文案；
  在第 0 步调用 `onPrevClick` 必须 `destroy()` + 写入 `localStorage` 已看过标记；
  第 3 步调用同一 hook 仍只走 `movePrevious()`、不销毁。**该测试做过变异校验**：
  把第 0 步分支改成 `if (false)` 后「结束引导」用例如期失败，说明测试不是空转。

### 3.3-2 423 锁定下「错误横幅 + 空状态」并存

**性质：确认是真缺陷，已修。**

原因：`DataTable` 在 `loading=false` 且 `rows=[]` 时渲染 `#empty` 插槽；加载失败后
`rows` 就是空数组，于是错误横幅和「还没有提示词模板」同时出现。

修复（错误态与内容互斥，沿用详情页 `v-if/v-else-if` 的既有写法）：

| 位置 | 改动 |
|---|---|
| `PromptTemplatesView.vue` | 错误横幅 `v-if`，列表卡片改 `v-else`；失败时同时清空 `templates` 与版本缓存，避免残留数据 |
| `PromptRequestEventsView.vue` | 同上（交接文档要求「顺手检查请求记录页」——确实有同样问题） |
| `TemplateEventPanel.vue` | 新组件一开始就按互斥写，不留同类问题 |

证据：`requestEventsView.spec.ts` 的「load 失败时不得声称列表为空」、
`templateEventPanel.spec.ts` 的「审计加载失败时不得声称无记录」。

### 3.3-3 423 提示是英文后端原文

**性质：机制已存在，本次补 key 并接入；未自创机制。**

- 后端 423 来自 `middleware/admin_compliance.go`：`{code: 'ADMIN_COMPLIANCE_ACK_REQUIRED',
  message: 'administrator compliance acknowledgement is required'}`；前端 `api/client.ts:146`
  会派发全局事件并弹出合规确认弹窗，但**页面自己的错误横幅**仍直接显示后端英文原文。
- 既有机制：`utils/apiError.ts` 的 `extractI18nErrorMessage(err, t, namespace, fallback)`，
  按 `<namespace>.<错误码>` 查 i18n，查不到就回退到后端原文（`extractApiErrorCode` 优先取
  `reason`，其次 `code`）。`payment.errors`、`admin.accounts.*` 等就是这么用的。
- **补 key**：新增 `admin.promptTemplates.errors.*`，共 30 条（`ADMIN_COMPLIANCE_ACK_REQUIRED`
  + 后端 `prompt_template*` 全部 29 个 `PROMPT_*` 错误码），en/zh 对齐。
- **接入**：把该功能**既有的 18 处** `extractApiErrorMessage(...)` 换成
  `extractI18nErrorMessage(err, t, 'admin.promptTemplates.errors', <原兜底文案>)`（新增的审计加载再走同一函数），
  涉及 `PromptTemplatesView.vue`(1)、`PromptTemplateDetailView.vue`(8)、`PromptRequestEventsView.vue`(1)、
  `CreateTemplateDialog.vue`(1)、`VersionPreviewDialog.vue`(1)、`components/admin/group/GroupPromptBindingModal.vue`(6)。
  这 6 个文件里已不再有 `extractApiErrorMessage` 调用。未登记的码仍然回退到后端原文，行为不劣化。

证据：`requestEventsView.spec.ts` 断言 423 场景下确实调用了
`admin.promptTemplates.errors.ADMIN_COMPLIANCE_ACK_REQUIRED` 的 i18n 查找；
`check:i18n` 保证 30 个新键 en/zh 都存在且非空。

**明确未做**：其他管理页面（如用户、分组等）遇到同一个 423 时仍显示后端英文原文。这是
跨页面的既有缺口，仓库里也不存在通用的「后端错误码」命名空间；按交接文档「不要自创机制」，
本次只补齐本功能自己的命名空间，没有借机重构全局。

### 3.4 额外发现（复核后端测试时查出，已修）：功能总开关的环境变量被静默忽略

**性质：本功能自己的真缺陷，已修。**

交接文档只要求跑 `go vet -tags=unit ./...`（它**不跑测试**）。补跑
`go test -tags=unit ./...` 后，`internal/config` 的守卫测试
`TestConfigKeysAreEnvReachable` 直接点名了本功能：

```
1 config keys have no default registered, so their environment variables are silently ignored:
      gateway.text_prompt_injection_enabled (bool)
```

该守卫存在的意义正是防这类事故（测试注释里举了 `image_storage` 凭据丢失的先例）：
viper 的 `Unmarshal` 只解码 `AllKeys()` 里的键，而 `AllKeys()` = 已注册默认值 ∪ 配置文件里的键；
`AutomaticEnv` 只能覆盖**已存在**的键，不能引入新键。所以 `GatewayConfig.TextPromptInjectionEnabled`
虽然声明了 `mapstructure:"text_prompt_injection_enabled"`，却没在 `setEnvReachableDefaults`
登记默认值 → 只要部署方的 `config.yaml` 里没写这个键，运维设的
`GATEWAY_TEXT_PROMPT_INJECTION_ENABLED=true` 就会被**静默丢弃**，功能看起来「打不开」。
（`scripts/prompt-injection-e2e.sh` 把开关写进了临时 config 文件，所以端到端脚本走的是
配置文件路径，掩盖了这个问题。）

**修复**（`backend/internal/config/config.go`，`setEnvReachableDefaults()` 末尾一行）：

```go
viper.SetDefault("gateway.text_prompt_injection_enabled", false)
```

只登记零值默认：默认行为仍是关闭，config.yaml / env 里的显式取值仍然优先；**不碰注入逻辑**
（`prompt_policy.go`、`prompt_adapter.go`、四个注入点均未改动）。已确认代码里没有任何地方
对本键用 `viper.IsSet` 判定（只有 `sticky_escape_enabled` 有那种依赖），因此登记默认值不会
把「未配置」误判成「已配置」。

**证据（含反证）**：

- `TestConfigKeysAreEnvReachable`：修前 FAIL，修后 PASS。
- 临时行为探针：`GATEWAY_TEXT_PROMPT_INJECTION_ENABLED=true` → `Load()` 后
  `cfg.Gateway.TextPromptInjectionEnabled == true`；不设 env → 仍为 `false`。
  **反证**：把那一行 `SetDefault` 注释掉后，上述两个测试**同时失败**，证明这不是空转断言
  （探针与反证用的临时文件已删除，未留在仓库里）。

---

## 4. 门禁与验证（全部在最终工作树上复跑）

| 命令 | 结果 |
|---|---|
| `./node_modules/.bin/vue-tsc --noEmit` | exit 0 |
| `./node_modules/.bin/eslint . --ext .vue,.js,.jsx,.cjs,.mjs,.ts,.tsx,.cts,.mts` | exit 0，无告警输出 |
| `./node_modules/.bin/vitest run src/i18n/__tests__/localeKeyCompleteness.spec.ts`（即 `check:i18n`） | 3 passed |
| `./node_modules/.bin/vite build` | `✓ built in 55.72s`，exit 0 |
| `go vet -tags=unit ./...`（backend） | exit 0 |
| `go test -tags=unit ./...`（backend，交接文档**没有**要求这条，但它是真正的守卫） | 3 个失败，见下 |

**前端补充回归**

- 新增测试 **17 项**全绿（`features/prompt-templates/__tests__/api.spec.ts` 3 +
  `templateEventPanel.spec.ts` 6 + `requestEventsView.spec.ts` 5 +
  `composables/__tests__/useOnboardingTour.skip.spec.ts` 3）。
- 全量 `vitest run`：**345 个文件 344 通过，2648 个用例 2645 通过**。
  3 个失败全部在 `src/api/__tests__/settings.authSourceDefaults.spec.ts`（平台配额 map
  实际 6 个平台、用例仍按 5 个断言）。该文件只 import `@/api/admin/settings`，
  而这两个文件本次都未改动（`git status` 为空），属**既有漂移，与本次改动无关**，未顺手修改。
- 引导「跳过」那处修复做过**变异校验**（改成 `if (false)` 后相应用例如期失败），不是空转测试。

**后端全量单测的 3 个失败（逐个定性，均非本次改动引入）**

| 失败 | 包 | 定性 |
|---|---|---|
| `TestConfigKeysAreEnvReachable` | `internal/config` | **本功能真缺陷** → 已修（见 3.4），修后 PASS |
| `TestLoadDefaultDatabaseSSLMode`、`TestLoadForBootstrapAllowsMissingJWTSecret` | `internal/config` | **本机环境产物**：见下方证据 |
| `TestOllamaProbeCallback_StaleLongDoesNotOverrideNewShort` | `internal/service` | 既有失败，Ollama 429 冷却 CAS 逻辑，与提示词功能无关；文件未改动；`-count=1` 连跑 3 次稳定失败（非 flaky） |

后两个 config 失败的环境证据：`configureConfigSource()` 里硬编码了
`addConfigPath("/app/data")`，在 Windows 上按当前盘符解析成 `G:\app\data`，于是测试
**读到了用户真实配置** `G:\app\data\config.yaml`（其中 `database.sslmode: disable`、
`jwt.secret` 有值）。用一个临时诊断测试打印 `viper.ConfigFileUsed()` 得到确证：

```
config file used: "G:\\app\\data\\config.yaml"
sslmode: "disable"
```

所以这两个用例断言的是「纯默认值」，却因为环境里存在该文件而失败；在没有
`/app/data/config.yaml` 的机器（如 Linux CI）上会通过。**这不是本次改动造成的，也没有去改
用户那份配置**（交接文档第 6 条明令禁止）。诊断用的临时测试文件已删除。

### 4.1 真实环境端到端验证（本轮已补做）

**怎么起的**：本地 PG 15.13 + Redis 都在跑，用 `scripts/prompt-injection-e2e.sh up` 建临时库
`s2a_inject_e2e`、写临时 config、起服务（8090）、造数据。**没有用真实上游**——本次只验证管理端
API 与页面，不需要出站请求，所以 `UPSTREAM_BASE/UPSTREAM_KEY` 传的是占位值。
因为要看浏览器，另用 `go build -tags embed` 重新构建（把刚 `vite build` 的前端资源嵌进去），
用同一份临时 config 重启服务。

**A. API 层（真实 HTTP，curl）**

| 检查 | 结果 |
|---|---|
| 未确认合规时访问管理接口 | `423` + `{"code":"ADMIN_COMPLIANCE_ACK_REQUIRED","message":"administrator compliance acknowledgement is required"}`（无 `reason` 字段，只有字符串 `code`——正是 3.3-3 依赖的取值路径） |
| 确认合规后访问 | `200` |
| 旧契约 `?limit=2` | `data` 是**数组** |
| 分页契约 `?page=2&page_size=3` | `data` 是**信封**：`{"items":[id 4,3,2],"total":7,"page":2,"page_size":3,"pages":3}`，offset 与 total 都正确 |
| 真实改一次模板元数据 | `200`，并产生真实审计事件 |
| `GET /prompt-templates/1/events` | `[{"action":"update_template","scope":"template","actor_id":1,"actor_name":"e2e@local.test","before_state":{"name":"e2e-marker"},"after_state":{"name":"e2e-marker-renamed"},...}]`——与面板的枚举映射、chip 键名假设**完全一致** |

**B. 浏览器层（headless Chrome 154 + CDP，中文界面，25 项断言全部通过）**

| 分组 | 断言 |
|---|---|
| 3.3-1 引导「跳过」 | 弹窗出现且「跳过」按钮 `disabled=False`（**修复前这里是 `True`**）→ 点击后弹窗关闭 → `localStorage` 记为 `true` |
| 3.1 分页 | 页脚显示「显示 1 至 20 共 45 条结果 每页: 20 1 2 3」；第 1 页首行 `req-e2e-45`；点第 2 页后首行 `req-e2e-25`（offset 正确）、`aria-current="2"` |
| 3.2 审计面板 | 真实渲染出「更新元数据 / 模板 / e2e@local.test / 变更前 名称 e2e-marker → 变更后 名称 e2e-marker-renamed」；默认**没有** `<pre>`；点「展开原始 JSON」后才出现含 `before_state`/`after_state` 的 JSON |
| 3.3-2 / 3.3-3（模板列表页） | 撤销合规确认后：横幅显示**中文**「需要先完成管理员合规确认，才能使用本页面。」、不再出现后端英文原文、不再同时声称「还没有提示词模板」、表格未渲染 |
| 3.3-2 / 3.3-3（请求记录页，交接文档点名要查的那页） | 同上 4 项全部通过，且错误态下不渲染分页控件 |

**C. 清理（已核验）**：`down` 后 `残留端口: 0`、`残留库: 0`，`/tmp/s2a-inject-e2e*` 已删，
Chrome 进程已杀；`G:\app\data\config.yaml` 哈希未变；工作树只剩预期的改动。
截图留在 `C:\Users\1\AppData\Local\Temp\shots\`（`a_tour_open`、`b_page1`、`b_page2`、
`c_audit`、`f_audit_panel`、`e_423_banner`、`g_events_423`），是本次运行的产物，未放进仓库。

### 4.2 仍然**没有**验证的部分

1. **注入链路没有重跑**：`scripts/prompt-injection-e2e.sh test`（真实上游 A/B 对照 + 标记检测）
   本轮没跑——注入逻辑零改动，且需要真实 `UPSTREAM_KEY`。交接文档里那份 M3 证据仍是上一轮的，
   我**没有**独立复现它。
2. **请求记录的数据是我用 SQL 造的**，不是真实网关流量产生的。也就是说
   `service/prompt_request_event.go`（运行期遥测写入）这条路径本轮没有被走到；
   分页验证的是「读」这一侧，不是「写」。
3. 浏览器验证用的是种子管理员（我给它设了一个已知密码）与内嵌 dist 构建，
   不是生产部署形态。

因此可以声称的是：**管理端 API 与三个页面的真实链路已端到端验证通过**；
**注入链路**与**遥测写入**这两段未在本轮复现。

## 5. 改动边界 / 清理

- **注入逻辑零改动**：`prompt_policy.go`、`prompt_adapter.go` 与四个注入点
  （`openai_gateway_cc_pipeline.go`、`openai_gateway_forward.go`、
  `openai_gateway_chat_completions.go`、`openai_gateway_passthrough.go`）`git status` 为空。
  因此**不需要**重跑 `scripts/prompt-injection-e2e.sh test`。
- `backend/` 只改了 **1 个文件**：`internal/config/config.go`（3.4 的一行 `SetDefault` +
  注释）。它不参与注入决策，只是让功能开关的环境变量可达；默认值仍是 false。
- `G:\app\data\config.yaml` 未触碰，SHA-256 仍为
  `d1319bf1cf9a5861267cb61beb4f834d56fb47b553ab1eb228886551994331bb`。
- **起过真实环境并已完整清理**：`scripts/prompt-injection-e2e.sh up` 建了临时库
  `s2a_inject_e2e`、临时 config（`/tmp/s2a-inject-e2e/config.yaml`）与 8090 端口的服务；
  `down` 之后核验为 `残留端口: 0`、`残留库: 0`，临时目录/二进制/日志已删，Chrome 进程已杀。
  全程用 `CONFIG_FILE` 指向副本，**没有碰** `G:\app\data\config.yaml`。
  `vite build` 输出到 `backend/internal/web/dist/`（被 `.gitignore` 忽略），
  该目录被 `-tags embed` 构建进临时二进制用于浏览器验证。
- 诊断/反证用的临时测试文件（`zz_diag_test.go`、`zz_env_probe_test.go`）、临时 hash 生成器
  （`backend/tmpgenhash/`）与临时测试二进制 `/tmp/config.test.exe` 均已删除，
  `git status` 里看不到它们。
- `pnpm install` 未执行，lockfile 未变。

## 6. 改动清单

```
M frontend/src/features/prompt-templates/api.ts
M frontend/src/features/prompt-templates/types.ts
M frontend/src/features/prompt-templates/PromptRequestEventsView.vue
M frontend/src/features/prompt-templates/PromptTemplateDetailView.vue
M frontend/src/features/prompt-templates/PromptTemplatesView.vue
M frontend/src/features/prompt-templates/components/RequestEventFilterBar.vue
M frontend/src/features/prompt-templates/components/CreateTemplateDialog.vue
M frontend/src/features/prompt-templates/components/VersionPreviewDialog.vue
M frontend/src/components/admin/group/GroupPromptBindingModal.vue
M frontend/src/components/Guide/steps.ts
M frontend/src/composables/useOnboardingTour.ts
M frontend/src/i18n/locales/en/admin/promptTemplates.ts
M frontend/src/i18n/locales/zh/admin/promptTemplates.ts
M backend/internal/config/config.go
A frontend/src/features/prompt-templates/components/TemplateEventPanel.vue
A frontend/src/features/prompt-templates/__tests__/{api,templateEventPanel,requestEventsView}.spec.ts
A frontend/src/composables/__tests__/useOnboardingTour.skip.spec.ts
A docs/plans/2026-10-08-frontend-finish-report.md   ← 本文件；docs/* 被 .gitignore 忽略，
                                                     需 git add -f 才会被跟踪（与 handoff 同）
```

## 7. 遗留（未做，供决策）

1. 本次改动**尚未提交**。门禁已复跑，可直接 `git commit`；注意本报告在
   `docs/plans/` 下而 `docs/*` 被忽略，需 `git add -f`（`docs/plans/2026-10-08-handoff.md`
   当初也是被强制加入的）。
2. **注入链路 / 遥测写入未复现**（见 4.2）：如需闭环，跑一次
   `UPSTREAM_BASE=… UPSTREAM_KEY=… bash scripts/prompt-injection-e2e.sh test`，
   让请求记录由真实网关流量产生后再看分页。
3. `settings.authSourceDefaults.spec.ts` 的 3 个既有失败建议单独修（平台数量已从 5 变 6）。
4. `internal/service` 的 `TestOllamaProbeCallback_StaleLongDoesNotOverrideNewShort` 是既有失败，
   与本次无关，建议单独排查（连跑 3 次稳定失败，不是 flaky）。
5. `internal/config` 那两个失败是本机 `G:\app\data\config.yaml` 被测试读到所致；若希望
   `go test` 在开发机上也可全绿，需要给测试隔离配置文件来源（例如测试里强制
   `CONFIG_FILE` 指向临时文件）——属独立议题。
6. 其他管理页面的 423 英文原文，若要全局统一，需要先立一个通用错误码命名空间再逐页接入——
   属于独立议题，不在本次范围。
7. 引导弹窗「跳过」的修复只动「第一步的退出路径」+ 两个欢迎步骤的按钮禁用配置；
   21 步的内容、顺序与交互（`data-tour` 钩子）未动。

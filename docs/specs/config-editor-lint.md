# 功能规格：配置编辑器格式校验

> 状态：开发中　·　关联 PRD：FR-75　·　分支：feature/fr-75-editor-lint

## 1. 背景与目标

配置中心（FR-1）在管理台用 Monaco 编辑器（`CodeEditor`）在页编辑配置内容，点保存经 FR-67 保存确认对话框后调 `publishConfig` 发布。
当前编辑器**不做任何客户端格式校验**：一份语法非法（如 YAML 用 Tab 缩进、引号 / 括号不闭合，或 JSON 多逗号）的内容可以一路点到「确认保存」，直到发布请求落到控制面（FR-27 服务端 schema 校验）甚至下发到 agent 才暴露错误，反馈链路长、用户改起来盲目。

本特性给编辑器接**客户端格式校验**：在浏览器即时解析当前编辑内容，解析失败时**就近行内标错**（行号 + 信息）并**禁用保存 / 发布入口**，合法才放行。把坏格式挡在发布之前、即时反馈，属第二期（P2）体验增强，增强 FR-1/FR-3。

与 FR-27 的关系：FR-27 是**控制面服务端**在 `Create`/`Publish`/`Rollback` 发布路径做「可解析 + 根为 map + 键非空」结构校验（最后一道权威闸）；FR-75 是**纯前端**的前置即时反馈，二者互补、不替代——前端可被绕过，服务端仍是安全权威。

## 2. 需求（要什么）

- 在 `CodeEditor` 编辑模式下对当前内容按声明格式（`tab.format`）做客户端格式校验，内容变更即时重算。
- 校验规则：
  - `format=json`：用 `JSON.parse` 解析；失败即非法（错误信息含位置时尽量映射到行号）。
  - `format=yaml`：**先查 `web/package.json` 是否已有 YAML 解析库**——经查**当前无 `js-yaml` 等 YAML 解析依赖**。按 .claude 规则 15（加依赖须先确认）**不擅自引入**，改为实现**轻量 YAML 结构校验**：禁 Tab 缩进、基本 `key:` 结构合法性、明显的括号 / 引号不闭合。
  - 其它格式（`properties` / `plaintext`）：不做格式解析（无明确语法约束，放行）。
  - 空内容：放行（该层不贡献，合法，与 FR-27 一致）。
- 解析失败 →
  - **禁用保存 / 发布入口**：`ConfigEditorPane` 的「保存」按钮禁用、Ctrl+S 唤起的保存被拦；合法才可发布。
  - **行内 / 就近标错**：编辑器旁展示错误条（行号 + 信息），用户可定位。
- 既有发布流程不回归：合法内容下保存确认对话框、`publishConfig`、Ctrl+S 行为不变。
- 不做（范围外）：
  - **不引入 js-yaml 等新依赖**（须用户确认；轻量校验已满足「挡明显坏格式」目标）。
  - 不做完整 YAML 1.2 语义校验（锚点 / 别名 / 多文档 / 复杂流式集合）——轻量校验只覆盖最常见错误，完整校验留待用户确认是否引库。
  - 不做业务语义校验（端口范围等）、不做 schema 注册（属 FR-27 边界外，另立 FR）。
  - 不改控制面 / agent / API 契约；纯前端。

## 3. 设计（怎么做）

- 新增纯函数模块 `web/src/lib/configLint.ts`：
  - `lintContent(format: string, content: string): LintError | null`——按格式分派，返回首个错误（`{ line: number; message: string }`）或 `null`（合法）。无副作用、可穷举单测。
  - JSON：`JSON.parse` 包裹 try/catch；从 V8 错误信息中尽力提取 `position`/`line` 映射行号，提取不到则归到第 1 行。
  - YAML 轻量校验：逐行扫描——① 行首缩进含 Tab → 报错（YAML 禁 Tab 缩进）；② 在非数组项 / 非续行的「映射行」上要求出现冒号分隔的 `key:` 结构；③ 跨行累计引号（`'` / `"`）与流式括号（`{} [] ()`）配对，文档结束未闭合 → 报错并指向起始行。注释（`#` 起）、空行、`---`/`...` 文档标记跳过。规则刻意保守，只逮明显坏格式、不误报常见合法 YAML。
- `CodeEditor`（编辑模式）：
  - 内部对 `value`/`language` 跑 `lintContent`，把结果通过新增可选回调 `onValidate?(error: LintError | null)` 上抛给父层（供禁用按钮）。
  - 编辑器下方渲染**错误条**（行号 + 信息），合法时不渲染。diff 模式不校验（只读对比）。
- `ConfigEditorPane`：编辑视图接 `CodeEditor` 的 `onValidate`，把 lint 错误状态上抛给 `ConfigsPage`；保存按钮在有 lint 错误时 `disabled` 并加提示 title。
- `ConfigsPage`：持当前 lint 错误状态；`requestSave`（含 Ctrl+S 路径）在有 lint 错误时不打开保存确认对话框（拦在发布前）。
- 不涉及架构决策（无新技术 / 新模式 / 不推翻 ADR，不加依赖），故**不写新 ADR**；纯前端、分层不变。

## 4. 任务拆分

- [ ] 写 `lib/configLint.ts` 单测（红）：JSON 合法 / JSON 非法（多逗号 / 缺引号）/ YAML 合法 / YAML Tab 缩进 / YAML 引号未闭合 / YAML 括号未闭合 / 空内容放行 / properties 放行
- [ ] 实现 `lintContent` 纯函数
- [ ] 写 `CodeEditor` 行内标错 + `onValidate` 上抛单测（红）
- [ ] `CodeEditor` 接入校验 + 错误条渲染
- [ ] `ConfigEditorPane` / `ConfigsPage` 接 lint 状态 → 非法禁用保存、拦 Ctrl+S
- [ ] i18n 文案（错误条标题 / 行号信息格式 / 保存禁用提示）
- [ ] 文档同步：本 spec、PRD 状态（已置开发中）、CHANGELOG 未发布段

## 5. 验收标准

- JSON 非法内容（多逗号 / 缺引号）→ 编辑器旁出行内错误（行号 + 信息）、保存按钮禁用、Ctrl+S 不弹保存确认。
- YAML Tab 缩进 / 引号未闭合 / 括号未闭合 → 同上禁用并标错。
- 合法 JSON / YAML、空内容、properties → 不标错、保存正常可用、发布流程不回归。
- `cd web && pnpm test` 全绿（含新用例）；`pnpm build` 通过。

## 6. 风险 / 待定

- **轻量 YAML 校验不等于完整 YAML 校验**：只逮 Tab 缩进 / 引号 / 括号 / 基本 `key:` 结构等明显错误，复杂 YAML（锚点 / 多文档 / 深层流式结构）可能漏判或误判。完整校验需引入 `js-yaml`——**已留待用户确认是否加依赖**（违反 .claude 规则 15 不擅自加）。漏判由 FR-27 服务端校验兜底，不会发坏到 agent。
- 前端 lint 是体验优化、非安全闸；服务端 FR-27 仍是权威。

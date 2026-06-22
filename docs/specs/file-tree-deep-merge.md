# 功能规格：文件树结构化深合并 + 按文件整文件豁免

> 状态：开发中　·　关联 PRD：FR-44　·　分支：feature/fr-44-file-tree-deep-merge

## 1. 背景与目标

通道B 文件树托管（FR-14，[ADR-0010](../adr/0010-file-tree-hosting-blob-channel.md)）当前对每个 `path` 做**整文件覆盖**——覆盖链 global←大区←小区←单服 上取层级最高的那一整份，绝不深合并。这让"100+ 异构服共享一份基线 + 各层小增量"无法表达：同一个 `config.yml` 小区只想改 3 个键，也得维护整份副本。而通道A（配置中心，FR-1）早有键级深合并，但需插件读 agent API，**无法接入的第三方插件只能走通道B**。

目标：把通道A 的深合并能力嫁接到通道B 的文件树形态上——**结构化文件跨四层按键深合并**，第三方插件零改、照旧从磁盘读到合并后的整文件。属 P2 治理增强。

## 2. 需求（要什么）

- 范围内：
  - **结构化文件深合并**：`path` 后缀为 `.yml`/`.yaml`/`.json`/`.properties` 的文件，跨 global/大区/小区/单服 四层**按键深合并**（复用 `internal/merge`：标量覆盖 / map 深合并 / list 整体替换 / 高层显式 `null` 删键 / 确定性键序与 md5）。
  - **非结构化整文件兜底**：其余后缀（`.allin`/`.js`/`.lang`/`.txt`/无后缀等）维持整文件覆盖（取最高层那份）。
  - **按文件整文件豁免**：每个文件可标记"整文件覆盖"，即使是结构化后缀也不深合并（保注释、不被重渲染打乱）——给注释敏感 / 不宜被合并的文件留逃生口。
  - 控制面复用 `merge` **渲染每服合并后的整文件**，经既有 `files/manifest`（md5）与 `files/content`（整文件内容）下发；**agent 镜像落盘逻辑零改**（仍是哑镜像、原子写、fail-static）。
  - manifest 的单文件 md5 = **合并后整文件**的 md5（非任一层原始内容的 md5）。
- 不做（范围外）：
  - 不引入按 dataId / path 的字段级 schema 注册表（合并规则与通道A 一致、保守）。
  - 不做注释保留的合并（结构化深合并必然重渲染、丢注释——靠"整文件豁免"规避，不引保注释 yaml 库，见 §6）。
  - 不改 agent、不改覆盖集（FR-15）通道、不改通道A。
  - 前端"设置豁免标记"的 UI 仅做最小（既有文件新建/发布表单加一个开关）或随 FR-45/46 完善；本 FR 重点是后端解析。

## 3. 设计（怎么做）

涉及架构决策（推翻 ADR-0010 决策1「绝不深合并」）→ 另写 [ADR-0029](../adr/0029-file-tree-structured-deep-merge.md)，此处不重复决策正文。

**单一改造点**：`internal/filetree` 的纯解析函数 `Resolve`。`files/manifest` 与 `files/content` 两端点都经 `FileEffectiveService.Resolve → filetree.Resolve` 取 `tree.Files`（[file_handler.go](../../internal/handler/file_handler.go) Content 复用同一结果），故只改 `Resolve` 一处，两端点同时生效。

- **格式探测**：导出纯函数 `FormatFromPath(path) (format string, structured bool)`，按后缀映射到 `merge.FormatYAML/JSON/Properties`；未知后缀 `structured=false`。发布期校验复用同一函数。
- **Resolve 改造**：候选按 `path` 分组；每个 `path`：
  1. 求该 path 覆盖链上**层级最高**的候选（winner）+ 扫描各层是否**任一标 `WholeFileOverride`**（path 级豁免）。
  2. **判定合并模式**——满足任一即 `whole-file`（取 winner 整文件、字节原样、不 parse/reserialize）：① **单层贡献**（`len(layers)==1`，无需合并、杜绝有损往返）；② 后缀非结构化；③ 任一层标豁免。否则 `deep-merge`。
  3. `deep-merge`（≥2 层结构化且未豁免）→ 按 scope 优先级**低→高**取各层 Content，调 `merge.MergeDataID(format, layeredLowToHigh)` 得合并整文件。
  4. `EffectiveFile.MD5 = md5(有效内容)`（合并模式重算、整文件模式即 winner 内容 md5）。
- **数据模型**：`model.FileObject` 增 `WholeFileOverride bool`（列 `whole_file_override`，`NOT NULL DEFAULT false`，基础类型、零方言、可切 Postgres）。AutoMigrate 加列，既有行默认 false。
- **写路径 + 发布校验**：`FileService.Create` / `Import` 透传 `WholeFileOverride`；admin `POST /admin/v1/files` 新增可选布尔字段 `wholeFileOverride`（缺省 false）、文件视图（List/Get）回显该字段。`validateFileContent(path, content)` 对结构化文件做 `merge.Parse` 解析校验，坏语法返 `CONTENT_SCHEMA_INVALID`（Create/Import/Publish 三写路径共用，与通道A 发布校验对齐）。
- **依赖流向**：`filetree` 包 import `internal/merge`（两者皆无副作用纯函数、merge 不反向依赖 filetree，无环）；`service` 复用 `filetree.FormatFromPath` + `merge.Parse` 做发布校验。

**判定规则的确定性**：合并模式由 层数 + 任一层 `WholeFileOverride` + 后缀决定，三者皆与遍历顺序无关；深合并复用 `merge` 的固定键序序列化 → 相同候选恒得相同 md5（长轮询比对依赖此幂等，与通道A 同源保证）。

## 4. 任务拆分

- [ ] [ADR-0029](../adr/0029-file-tree-structured-deep-merge.md)：文件树结构化深合并（取代 ADR-0010 决策1）；ADR-0010 决策1 标"已被 ADR-0029 取代"。
- [ ] PRD FR-44 → 开发中；ARCHITECTURE §5.1（整文件覆盖 → 结构化深合并 + 兜底 + 豁免）；API §6/§7（manifest/content 语义：md5 为合并后整文件、content 为合并结果）；adr/README 加 0029。
- [ ] `internal/filetree`：`formatFromPath` + `Resolve` 深合并改造（穷举单测先行）。
- [ ] `internal/model`：`FileObject.WholeFileOverride` 字段 + AutoMigrate。
- [ ] `internal/service` / `internal/handler`：Create/Import 透传 `wholeFileOverride` + API 字段 + 单测。
- [ ] 文档同步：PRD / ARCHITECTURE / API / CHANGELOG。

## 5. 验收标准

- 同 path 的 `app.yml`：global 基线 `{a:1,b:{x:1}}` + 小区增量 `{b:{y:2}}` + 单服 `{a:null,c:3}` → 某服拉到 `{b:{x:1,y:2},c:3}`（标量覆盖、map 深合并、`null` 删 a 键）。
- list 整体替换：高层 list 整替低层 list（不拼接）。
- **单层短路**：单层结构化文件（含前导零 `007` / 版本 `1.10` / 日期 / 纯注释 / JSON 大整数）**字节原样透传**，逐字节等于原文、md5 = 原文 md5（杜绝有损往返）。
- **path 级豁免**：任一层标 `wholeFileOverride=true` 即整 path 整文件覆盖、取最高层原文（即便 winner 未标）；逐字节不丢。
- **发布校验**：结构化文件坏语法在 Create/Publish/Import 即被拒 `CONTENT_SCHEMA_INVALID`；非结构化文件不做解析校验。
- 非结构化文件（`.allin`/`.js`）整文件覆盖（取最高层）。
- manifest 单文件 md5 = 合并后整文件 md5；相同候选两次解析 md5 一致（幂等、不误唤醒长轮询）。
- 穷举单测覆盖上述 + 混合层 + 空层不贡献 + 坏结构化内容的处理。
- 受影响组件测试全绿（`go test ./...` + `-tags=integration`）。
- **真机**：第三方插件目录托管一份 yml 基线 + 单服增量，agent 落盘为合并结果、插件读到并热重载生效（属发版前 E2E 门）。

## 6. 风险 / 待定

- **多层结构化值归一化（已知、已与用户确认；发版前 review 收窄）**：**单层**结构化文件已由单层短路字节透传、不受影响；**多层**结构化文件按键合并必经 parse→reserialize，不仅丢注释 / 原键序，还**归一化值**（`007`→`7`、`2026-06-22`→时间戳、`1.10`→`1.1`、JSON 大整数精度丢失）。多层结构化文件升级后生效结果可能变；不能容忍值归一者须在某层标 `wholeFileOverride`（= 放弃该 path 合并）。现网升级前复核**多层**结构化托管文件。不引保注释 / 保类型的 yaml 库（本期）。
- **升级 churn**：`EffectiveFile.MD5` 语义由「原始内容 md5」改为「合并后内容 md5」，控制面升级后多层结构化文件 manifest md5 变化，agent 首轮一次性重取重写盘（叠加 `FileTreeApplier` 整轮 fail-static，大文件树升级窗口需关注）。单层文件 md5 不变（透传），不在 churn 范围。
- **坏结构化内容（已定，见 [ADR-0029](../adr/0029-file-tree-structured-deep-merge.md) 决策5/6）**：发布期 `merge.Parse` 校验已拒坏语法入库（决策6）；运行期某层仍解析失败时 **该 path 回退为整文件取 winner**，不抛错中断整树解析（`Resolve` 保持纯函数、确定性，一坏文件不拖垮整棵树），回退为兜底而非常态。

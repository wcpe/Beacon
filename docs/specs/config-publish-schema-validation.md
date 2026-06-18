# 功能规格：配置发布前 schema/类型校验

> 状态：开发中　·　关联 PRD：FR-27　·　分支：feature/fr-27-config-validate

## 1. 背景与目标

配置中心（FR-1）按 scope 覆盖链（global ← group ← zone ← server）把多层内容深合并后下发给目标子服。
发布路径（FR-1/FR-3 的 `Create` / `Publish` / `Rollback`）当前只校验：格式白名单、内容大小、按声明格式可解析。
这意味着一条**能解析但结构非法**的配置仍可发布，下发后导致目标服 apply 异常：

- 顶层是标量（如 `42`、`"text"`）或列表（如 `- a`）——而非键值文档。这类内容在深合并里会**整体替换**（见 `internal/merge/merge.go` 规则），静默冲掉其它覆盖层，且 agent 端按"键→值"加载时直接异常。
- 含**空键**（如 YAML `"": 2`、properties 无名键）——agent 端键查找失败。

本特性在发布前补齐**结构与类型校验**，不通过则拒绝发布并返回明确错误（统一 apperr → 对外响应），把坏配置挡在下发之前。属第二期（P2）治理增强，增强 FR-1/FR-3。

## 2. 需求（要什么）

- 发布前（`Create` / `Publish` / `Rollback` 三条写路径）对配置内容做结构与类型校验。
- 范围内：
  - **格式**：按声明格式可解析（已由既有 `validateContent` 覆盖，本特性复用）。
  - **结构**：非空内容的顶层必须解析为键值映射（map），不得是裸标量或列表。
  - **类型 / 必填项**：映射的键必须是非空字符串（递归进嵌套 map 校验），不得出现空键 / 仅空白键。
  - 校验落在控制面 service 层；校验细节为 `internal/merge` 的无副作用纯函数（可穷举单测）；handler 不碰校验细节。
  - 校验失败统一返回新的业务错误码 `CONTENT_SCHEMA_INVALID`（HTTP 422），经 handler render 转对外响应。
- 不做（范围外）：
  - **不引入按 dataId 声明的 schema 注册表 / 模型 / 仓储 / 管理 API / 前端**——FR-27 是对既有发布路径的增强，不是"schema 管理系统"。需要时另立 FR。
  - 不做业务语义校验（如"端口必须在 1024-65535"）——那需要外部 schema，超出本期。
  - 不改 agent 端；agent 仍按 fail-static 处理任何已下发内容。

## 3. 设计（怎么做）

- 在 `internal/merge` 新增纯函数 `ValidateSchema(format, content string) error`：
  - 空内容（解析为 nil）直接放行（该层不贡献，合法）。
  - 解析后顶层非 `map[string]any` → 返回结构错误（标量 / 列表根）。
  - 递归校验所有 map 键非空（去空白后不为空）→ 否则返回空键错误。
  - 返回的是 `merge` 包内的语义错误（哨兵 error），由 service 层映射为 apperr。
- service 层 `validateContent` 在既有"格式 + 大小 + 可解析"之后追加调用 `merge.ValidateSchema`，失败映射为 `apperr.ErrContentSchemaInvalid`。三条写路径（`Create`/`Publish`/`Rollback`）均经 `validateContent`，因此一处接入即全覆盖。
  - 注意：`Rollback` 取历史版本内容重发，历史内容理论上已通过校验；为防御历史脏数据，仍在 `validateContent` 统一兜底。
- 新增 `apperr.ErrContentSchemaInvalid`（422，`CONTENT_SCHEMA_INVALID`）。
- 不涉及架构决策（不新增技术/模式/不推翻既有 ADR），故**不写新 ADR**；纯函数仍在 `merge` 包、错误仍走 apperr、分层依赖不变。

## 4. 任务拆分

- [x] 写 `internal/merge` 失败单测（合法通过 / 顶层标量 / 顶层列表 / 空键 / 嵌套空键 / properties 空键 / 空内容放行）
- [x] 写 service 层单测（发布坏结构被 `CONTENT_SCHEMA_INVALID` 拦）
- [x] 实现 `merge.ValidateSchema` 纯函数
- [x] 新增 `apperr.ErrContentSchemaInvalid` 并在 `validateContent` 接入
- [x] 文档同步：本 spec、PRD 状态（已预置开发中，不改）、API.md 错误码、ARCHITECTURE 校验说明、CHANGELOG 未发布段

## 5. 验收标准

- 合法键值文档（yaml/json/properties）发布通过。
- 顶层标量 / 顶层列表的 yaml、json 发布被拦，返回 `CONTENT_SCHEMA_INVALID`。
- 含空键的内容发布被拦，返回 `CONTENT_SCHEMA_INVALID`。
- 空内容（该层不贡献）发布不被结构校验拦截（仍按既有逻辑处理）。
- `go test ./internal/merge/ ./internal/service/` 全绿；`go build ./...` 通过。

## 6. 风险 / 待定

- 结构规则刻意保守：仅"根必须是 map + 键非空"，不臆造业务规则，避免镀金。若后续需要按 dataId 的字段级 schema，另立 FR 并评估是否引入外部 schema 依赖。

# 功能规格：文件树结构化无损深合并

> 状态：开发中　·　关联 PRD：FR-57（增强 FR-44）　·　分支：feature/fr-57-lossless-filetree-merge

## 1. 背景与目标

[FR-44](../PRD.md)/[ADR-0029](../adr/0029-file-tree-structured-deep-merge.md) 把通道B 结构化文件做成跨四层按键深合并，**复用了配置中心的 `internal/merge.MergeDataID`**。该 merge parse 成类型模型（`map[string]any`）再 serialize，对**多层**文件**归一化叶子标量值**并丢注释：`007`→`7`、`1.10`→`1.1`、`2026-06-22`→时间戳、JSON 大整数失精度。通道B 托管的是第三方插件**死认文本**的磁盘配置，归一化是 bug。

目标：**只把文件树通道的多层合并改为无损**（保标量原文 token / 精度 / 注释），配置中心维持 `MergeDataID` 有损。合并语义完全不变，只改保真度。属 P2 治理增强。决策见 [ADR-0034](../adr/0034-file-tree-lossless-merge.md)。

## 2. 需求（要什么）

- 范围内：
  - **三格式无损多层合并**：`.yml`/`.yaml`/`.json`/`.properties` 多层按键深合并时，叶子标量保留原文 token、注释保留（YAML/properties）。
  - **合并语义不变**：标量覆盖 / map 深合并 / list 整替 / 高层 `null` 删键 / 确定性键序与 md5 幂等，与 FR-44 完全一致。
  - **filetree 接线**：深合并分支改调无损版；单层短路字节透传、wholeFileOverride 豁免、坏内容回退整文件三条不变。
  - **provenance 一致**：`ResolveWithProvenance` 每文件 `content`/`md5` 恒等于 `Resolve`（FR-45 交叉测试）；逐键来源判定不变。
- 不做（范围外）：
  - 不改配置中心通道A（`MergeDataID`/`MergeDataIDWithProvenance` 行为零变化）。
  - 不改合并语义、不引字段级 schema、不改 agent / 覆盖集通道 / 数据模型。
  - 不引第三方"保注释合并"库（YAML 用已依赖的 `yaml.v3` 节点能力、JSON/properties 用标准库）。
  - 不追求纯注释文件在**多层**合并下保注释（filetree 单层即字节透传、不入合并路径；多层合并丢纯注释层属既有 churn）。

## 3. 设计（怎么做）

涉及架构决策（取代 ADR-0029「值归一化可接受」一条）→ 见 [ADR-0034](../adr/0034-file-tree-lossless-merge.md)，此处不重复决策正文。

**新增无损入口（`internal/merge`，与 `MergeDataID` 隔离）**：

- `MergeDataIDLossless(format, layeredLowToHigh []string) (string, error)`：多层内容低→高无损深合并为渲染文本。空层不贡献、全空返空串。
- `MergeDataIDLosslessWithProvenance(format, layers []ProvLayer) (content string, sources, deletions []KeyProvenance, err error)`：无损渲染 `content` + 逐键来源（来源判定复用现有类型模型 provenance 机制——来源是「哪层拥有该键」的**语义**问题，与表示无关；content 走无损渲染）。

**三格式做法**：

- **YAML**：`yaml.Node` 节点级递归深合并，不 Unmarshal 到 `interface{}`。
  - MappingNode：按 key 合并。双方同 key 且都是 MappingNode → 递归；override 值为 `!!null` → 删该 key；否则（标量/序列/类型不一致）override 整替。对 key/value 对按 key 排序保确定性键序，注释（Head/Line/Foot）随节点搬。
  - ScalarNode：保留 override 节点（`Value` 即原文 token，`Tag`/`Style` 不动）→ 不归一化。
  - SequenceNode：整替（取 override 整个序列节点）。
  - 序列化：`yaml.Encoder` + `SetIndent(2)` 对 `*yaml.Node` emit（确定性、保 token 与注释）。空/纯注释/纯空白文档 = 零 Kind 节点，按"不贡献"处理。
- **JSON**：`json.Decoder` + `UseNumber()` 解析（数字成 `json.Number` 不失精度），合并语义同上，`json.Marshal` 序列化（map key 自动排序 = 确定性、`json.Number` 按原文 emit）。
- **properties**：行式模型，保留每个 key 的前置注释行（`#`/`!`）与原值文本；按 key 合并（override 替值、`null` 删键）、键字典序输出、注释随键。

**filetree 接线**：`resolve.go` 深合并分支 `merge.MergeDataID` → `merge.MergeDataIDLossless`；`provenance.go` 深合并分支 `merge.MergeDataIDWithProvenance` → `merge.MergeDataIDLosslessWithProvenance`。其余分支（单层短路 / 豁免 / 坏内容回退）一字不动。

**md5 语义**：多层结构化文件合并后 md5 现基于**无损渲染内容**（比 FR-44 有损又变一次）→ 升级首轮一次性重取重写盘（预期、内容更正确）。单层文件 md5 不变（透传）。

**依赖流向**：无损实现仍在 `internal/merge`，`filetree → merge` 单向、无环；纯函数、无副作用。

## 4. 任务拆分

- [ ] [ADR-0034](../adr/0034-file-tree-lossless-merge.md)：文件树无损深合并；ADR-0029「值归一化可接受」标"已被 ADR-0034 取代"；adr/README 加 0034。
- [ ] PRD §4 加 FR-57 行（开发中）；ARCHITECTURE §5.1 最小补无损说明。
- [ ] `internal/merge`：`MergeDataIDLossless` + `MergeDataIDLosslessWithProvenance`（YAML 节点级 / JSON UseNumber / properties 保注释）；穷举单测先行。
- [ ] `internal/filetree`：深合并分支改调无损版（两处）。
- [ ] 文档同步：PRD / ARCHITECTURE / CHANGELOG。

## 5. 验收标准

- **三格式值保真 round-trip**：`007` 保 `007`、`1.10` 保 `1.10`、`2026-06-22` 保原样、JSON 大整数 `123456789012345678` 不失精度——多层合并后逐键值为原文 token。
- **YAML 注释保留**：头/行/脚注释多层合并后随其归属键保留。
- **properties 注释保留**：key 前置注释行随键保留。
- **合并语义不变**：标量覆盖 / map 深合并 / list 整替 / `null` 删键，与 FR-44 现有语义用例对齐。
- **确定性键序 / md5 幂等**：相同候选两次合并内容与 md5 一致。
- **单层短路 + 豁免不变**：单层字节透传、任一层豁免整文件覆盖，逐字节不丢。
- **provenance 与 Resolve 交叉一致**：`ResolveWithProvenance` 每 path 的 `content`/`md5` 恒等于 `Resolve`。
- **无损 vs MergeDataID 语义相等交叉**：无损渲染再 parse 成类型模型与 `MergeDataID` 的类型模型逻辑相等（值相等、忽略文本表示差异）——钉死"无损只改表示、不改语义"。
- 受影响组件测试全绿（`go test ./...`，含 `-tags=integration`/`-tags=e2e` 不破坏编译）。
- **真机**：第三方插件目录托管含前导零 / 注释的多层 yml，agent 落盘为保真合并结果（属发版前 E2E 门，主控会后跑）。

## 6. 风险 / 待定

- **升级 churn（一次性）**：多层结构化文件 manifest md5 由有损渲染 md5 改为无损渲染 md5，控制面升级后 agent 首轮一次性重取重写盘；单层文件 md5 不变。
- **YAML 排版收敛**：缩进收敛为 2 空格、行内注释前空白被 emitter 规整——仅影响排版与 md5、不影响值/注释保真。
- **纯注释 / 空文档边界**：解析为零 Kind 节点按"不贡献"处理；多层合并丢纯注释层（filetree 单层字节透传不入此路径，属既有 ADR-0029 churn）。

# ADR-0034：文件树通道改无损深合并（取代 ADR-0029「值归一化可接受」一条）

**状态**：已接受

> 本 ADR **取代 [ADR-0029](0029-file-tree-structured-deep-merge.md) 决策中「多层结构化文件按键合并经 parse→reserialize 归一化值、丢注释可接受」那一条**。ADR-0029 的合并语义（决策1~6：结构化分流 / 非结构化兜底 / path 级豁免 / 控制面渲染 agent 零改 / 坏内容降级 / 发布期校验）**全部继续有效、不变**——本 ADR 只把「多层合并的渲染保真度」从有损升级为无损，不动覆盖链合并语义本身。

## 背景

[ADR-0029](0029-file-tree-structured-deep-merge.md) 把通道B 结构化文件（`.yml`/`.yaml`/`.json`/`.properties`）做成跨四层按键深合并，**复用了配置中心（通道A）的 `internal/merge.MergeDataID`**。该 merge 把文件 parse 成通用类型模型（`map[string]any` / `interface{}`）再 serialize，对**多层**文件会**归一化叶子标量值**：`007`→`7`（前导零丢失）、`1.10`→`1.1`（版本号当浮点）、`2026-06-22`→时间戳、JSON 大整数失精度（`…678`→`…680`），并**丢失注释与原键序**。

ADR-0029 当时把这一损失记为「已知后果、可接受」，并以「单层短路字节透传 + 整文件豁免」收窄受影响面。但通道B 托管的是**第三方插件死认文本的磁盘配置**：前导零的邮编、字符串版本号、字符串化的大雪花 ID、带说明注释的配置，被归一化后语义被悄悄改写——对这类文件，归一化是 bug 而非可接受的代价。FR-44 落地后的真实托管场景要求**多层合并也保真**。

## 决策

**只把文件树通道（通道B）的多层结构化合并改为无损**，配置中心（通道A）维持现状：

1. **新增无损合并入口、不动 `MergeDataID`**：在 `internal/merge` 新增纯函数 `MergeDataIDLossless(format, layeredLowToHigh)` 与 `MergeDataIDLosslessWithProvenance(format, layers)`，供 `internal/filetree` 调用。配置中心仍用 `MergeDataID`/`MergeDataIDWithProvenance`，**行为零变化**。
2. **合并语义完全不变**：标量覆盖 / map 深合并 / list 整体替换 / 高层显式 `null` 删键 / 确定性键序（保 md5 幂等）。**只改保真度**——叶子标量保留原文 token、注释随节点保留。无损结果再 parse 成类型模型后，须与 `MergeDataID` 的类型模型**逻辑相等**（值相等、忽略文本表示差异），由交叉测试钉死「无损只改表示、不改语义」。
3. **三格式各自无损**：
   - **YAML**：用 `gopkg.in/yaml.v3` 的 `yaml.Node` 做**节点级递归深合并**，不 Unmarshal 到 `interface{}`。MappingNode 按 key 合并（双方同 key 且都是 Map → 递归，否则 override 整替，override 为 `!!null` → 删键）、ScalarNode 直接保留 override 节点（`Value` 即原文 token、`Tag`/`Style` 保留 → 不归一化）、SequenceNode 整替；对 MappingNode 的 key/value 对按 key 排序保确定性键序，注释（HeadComment/LineComment/FootComment）随节点搬（含被深合并触碰的中间层 map 的**键上区块注释**：同 key 在多层都出现时，key 节点用高层、其注释字段为空则回退低层，避免低层键注释被丢）。**顶层整层为 `!!null`（如单行 `null`/`~`）按「不贡献」处理、保留低层**（对齐有损 `Parse("null")=nil`；区别于 map 内某键值为 null 的删键）。序列化对 `*yaml.Node` 直接 emit。
   - **含锚点 / 别名 / `<<` 合并键的 YAML 不做深合并，整文件回退到最高层贡献层**：节点级合并重建 MappingNode/SequenceNode 时无法安全保留锚点定义与别名引用关系（手写 `<<` 展开 + 别名内联易产出悬空 `*x` / `!!merge` 等**不可解析的坏文件**）。故合并前递归检测任一贡献层是否含 `AliasNode` / 锚点（`node.Anchor != ""`）/ `<<` 合并键，命中即**该文件回退整文件取最高层贡献层原文**（复用「坏内容回退取最高层 winner」同一语义）。确定性优先、绝不产坏文件；属罕见场景。
   - **JSON**：`json.Decoder` + `UseNumber()` 解析（数字成 `json.Number` 字符串背书、不失精度），合并语义同上，`json.Marshal` 序列化（map key 自动排序 = 确定性、`json.Number` 按原文 emit）。JSON 无注释。
   - **properties**：行式模型，保留每个 key 的前置注释行与原值文本，按 key 合并（override 替值）、确定性键序输出、注释随键。不引新库。**properties 无删键能力**：值 `null` 是普通字符串值、原样保留——与有损 `MergeDataID`（properties 经 `map[string]any`、`"null"` 是字符串、`DeepMerge` 永不删）**完全一致**；不凭空发明「值 `null` = 删键」语义（否则会静默吞掉合法值 `flag=null` 并使 provenance 的 sources 与 content 错位）。
4. **filetree 接线**：`internal/filetree` 的深合并分支由调 `MergeDataID`/`MergeDataIDWithProvenance` 改调无损版。**单层短路字节透传、wholeFileOverride 豁免、坏内容回退整文件**三条逻辑保持不变。`ResolveWithProvenance` 与 `Resolve` 的每文件 `content`/`md5` 仍须逐一致（FR-45 交叉测试守护）。

## 理由

- **保真即正确**：通道B 是第三方插件的磁盘配置镜像，文本表示就是语义，必须无损。无损合并消除 ADR-0029 后果里列举的全部值归一化与注释丢失。
- **通道A 不受波及**：配置中心是类型化配置存储、归一化可接受，且改它会让全集群配置首轮 md5 变→全量重拉，代价不可接受。两套入口隔离 → 改一边不动另一边。
- **不引重型依赖**：YAML 复用仓库已依赖的 `yaml.v3`（`yaml.Node` 即其原生能力）；JSON/properties 用标准库。无新第三方包。
- **纯函数、可穷举测**：无损合并仍是无副作用纯函数，三格式值保真 + YAML 注释 + 语义不变交叉，全部穷举单测。

## 后果

- `internal/merge` 新增无损合并实现（YAML 节点级 / JSON UseNumber / properties 保注释），与 `MergeDataID` 隔离。`internal/filetree` 改调无损版。
- **升级 churn（一次性）**：多层结构化文件的合并后 md5 现基于**无损渲染内容**（比 ADR-0029 的有损渲染又变一次）→ 控制面升级后多层结构化文件 manifest md5 变化，agent 首轮一次性重取重写盘。属预期、且内容更正确（CHANGELOG 写明）。**单层文件 md5 不变**（仍字节透传）。
- ADR-0029 的「值归一化可接受」一条标注为「已被 ADR-0034 取代」；ADR-0029 合并语义（决策1~6）不变、继续有效。
- YAML 边界（已用 round-trip 单测钉死）：纯注释 / 空 / 纯空白文档解析为零 Kind 节点（无内容），多层合并里按「不贡献」处理（纯注释文件在 filetree 是单层字节透传、不入合并路径）；注释随其归属节点（头/行/脚）搬，排序时一起移动；序列化缩进收敛为 2 空格（确定性，仅影响排版与 md5、不影响值保真）。

## 备选方案

- **维持 ADR-0029 有损合并**：对死认文本的第三方配置是 bug。被本 FR 否决。
- **把配置中心也改无损**：通道A 是类型化存储、归一化可接受，且改它使全集群配置首轮 md5 变、全量重拉，代价不可接受。否决——只改文件树通道。
- **引第三方"保注释合并"库**：`yaml.v3` 的 `yaml.Node` 已能保注释/保 token，无需新依赖；JSON/properties 标准库即可。否决引库。
- **在 agent 侧无损合并**：违 agent 哑/零改与控制面渲染边界、且要把无损合并搬进 Kotlin（双实现漂移）。否决——合并留控制面。

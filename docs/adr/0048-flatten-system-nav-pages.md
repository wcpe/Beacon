# ADR-0048：管理台「系统」区改为扁平独立页（取代 ADR-0043 的设置聚合 + 旧页折叠 + 二级子 tab）

**状态**：已接受

## 背景

[ADR-0043](0043-admin-nav-grouping-and-settings-aggregation.md) 把管理台「系统」区做成「设置聚合页（`/settings` 三块顶层 tab：运维设置 / 系统信息 / 系统设置）+ 旧页折叠进子 tab + 块内二级子 tab + 嵌套子路由深链」的信息架构（FR-94/FR-95 落地）。真机使用后用户否决该 IA：

- 「设置聚合页 + 两级 tab（顶层块 tab + 块内子 tab）」层级过深、点击路径长，运维找一个设置要先选块再选子 tab；
- 控制面健康 / 密钥管理 / 环境管理被折叠进设置子 tab 后，失去独立入口的直达性，反而比平铺更难找；
- 二级 tab 在视觉与交互上叠床架屋，与「侧栏 5 组手风琴」已提供的层级重复。

用户要求：把「系统」区**全部拍平为独立导航页**，删掉整套聚合页、旧页折叠与两级 tab。

本决策只改前端信息架构与路由，不动后端契约（FR-61/82/98/99/100/101 端点不变）。

## 决策

1. **「系统」导航组 = 5 个扁平独立页**，各自独立路由、各自独立页面，互不折叠 / 不嵌套 / 不重复：

   | 导航项 | 路由 | 页面 |
   |---|---|---|
   | 运维设置 | `/settings` | 单页（6 域一级 tab + 逐项编辑 / 恢复默认 + 批量保存） |
   | 版本与更新 | `/system/version` | 新页（版本 / 渠道 / 检查 / 更新 / 代理 / 更新设置） |
   | 控制面健康 | `/system` | 独立 `SystemObservabilityPage`（FR-82，补详细明细） |
   | 密钥管理 | `/api-keys` | 独立 `ApiKeysPage`（FR-42） |
   | 环境管理 | `/namespaces` | 独立 `NamespacesPage`（FR-53） |

2. **删除聚合 / 折叠 / 占位组件**：移除 `SettingsAggregate`（原 `SettingsPage` 聚合壳）、`SystemInfoBlock`、`SystemConfigBlock`、`OpsSettingsBlock`、`PlaceholderTab` 及其测试；`/settings/ops|system-info|system-config` 嵌套子路由与 `/settings`→`/settings/ops` 重定向一并删除。运维设置内容回收进 `SettingsPage` 单页。逐项编辑共享原语 `settingsEditing`（草稿 / dirty / 批量保存 / SettingRow）保留，由 `SettingsPage` 单页复用。

3. **运维设置回归单页**：`/settings` 直接渲染 `SettingsPage`——6 个 key 前缀域（health/metric/longpoll/alert/log/reverse-fetch）以**一级 tab** 呈现（禁止两级 tab），保留 FR-62/FR-77 的顶层集中草稿 + dirty + 逐项恢复默认 + 页脚批量保存（跨域统观全部脏项）。`update.*`（渠道 / 自动检查 / 周期 / 代理）**不在本页**，挪到版本与更新页。

4. **新「版本与更新」独立页 `/system/version`**：纵向分区合并「版本信息 + 渠道选择（stable/rc，用户可自由切，写 `update.channel` 热生效后重查）+ 立即检查（`?force=true`）+ release 日志（纯文本安全渲染，禁 `dangerouslySetInnerHTML`）+ 立即更新（FR-76 二次确认 → `POST /system/update` → 轮询进度 → FR-78 重连回显）+ 网络代理（`update.proxy-url` 表单，脱敏回显 / 未改不覆盖，承 ADR-0047）+ 更新设置（`update.auto-check-enabled` 开关 + `update.check-interval-hours` 1–168）」。复用既有 `useUpdateCheck` hook 与 FR-99 端点；原 `UpdateModal` / `VersionInfoTab` 逻辑迁入本页后删除。

5. **页眉版本徽章改为跳转**：`SystemHeader` 版本徽章（含有更新时的小红点）点击改为 `navigate('/system/version')`，不再弹 `UpdateModal`。`useUpdateCheck` 的低频轮询保留以喂红点。

6. **导航单一真源同步**：`web/src/lib/navModel.ts` 的 system 组 leaves 改为上述 5 个独立路由，删去 `/settings/system-config?tab=...` 这类深链 leaf；命令面板（CommandPalette）导航目标随 navModel 自动更新。侧栏 `NavLink` 加 `end` 做逐项精确高亮（杜绝 `/system` 前缀误命中 `/system/version`）。

## 理由

- **层级够用即止**：侧栏 5 组手风琴（ADR-0043 决策 1，保留）已提供一层分组；再叠「聚合页 + 块 tab + 子 tab」是第二、三层冗余层级，违背简单优先。扁平独立页让每个系统能力一跳直达。
- **直达性**：控制面健康 / 密钥 / 环境是高频独立运维入口，独立路由比折叠进设置子 tab 更易达、可深链、可被命令面板检索。
- **版本与更新单独成页**：在线更新涉及渠道切换 / release 日志 / 进度 / 代理 / 策略多块内容，模态框承载局促，独立页更宽裕，也让页眉红点点击有明确去处。
- **不破坏后端**：纯前端 IA 调整，后端 FR-61/82/98/99/100/101 端点与契约不变。

## 影响

- **取代关系**：取代 ADR-0043 的「设置区聚合页（决策 3）+ 旧页折叠（决策 7）+ 二级子 tab（决策 4/5 的子 tab 呈现）」；ADR-0043 的「5 组手风琴侧栏导航（决策 1）+ NAV 单一真源收敛（决策 2）」仍有效保留。ADR-0043 正文不改，仅状态行注记取代关系。
- **路由变更**：`/settings/ops|system-info|system-config` 移除；`/system`、`/api-keys`、`/namespaces` 由重定向恢复为直接渲染独立页；新增 `/system/version`。无对外 API 变更，无需迁移。
- **删除文件**：`SystemInfoBlock`/`SystemConfigBlock`/`OpsSettingsBlock`/`PlaceholderTab`/`VersionInfoTab`/`UpdateModal` 及相关测试。
- **关联**：FR-94（运维设置单页 + 版本与更新独立页）、FR-95（系统区扁平独立页）。

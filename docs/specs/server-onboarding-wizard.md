# 功能规格：新服接入引导向导

> 状态：开发中　·　关联 PRD：FR-85　·　分支：feature/fr-85-server-onboarding

## 1. 背景与目标

新接入一台 MC 服到 Beacon 时，运维当前要手工对照 agent `config.yml` 模板逐项填 `identity.namespace / server-id / group-hint / address`（或用 FR-41 的 `BEACON_AGENT_IDENTITY_*` 环境变量在 run 脚本里覆盖），还要记得「serverId 环境内唯一、重复会被控制面拒绝」「zone 由控制面权威指派、agent 不声明」这些约定，门槛高且易错（serverId 撞了要等 agent 启动报错才发现，zone 漏配则新服一直「未分配」）。

本功能在服务器页加一个「添加服务器」引导向导：填几个字段就生成可直接复制粘贴的 agent 配置片段（`config.yml` identity 段 + run 脚本 env 段），并在向导内先校验 serverId 不重复、可选地预先建好 zone 指派，把「新服接入」从分散的手工操作收敛为一条引导路径。属 P2 运维体验优化。

## 2. 需求（要什么）

- 服务器页加「添加服务器」按钮，打开分步引导向导（对话框）。
- 第 1 步「填写身份」：环境（namespace，下拉自 `listNamespaces`）、serverId、角色（bukkit | bungee）、大区（group，作为 `group-hint`）、对外地址（address，ip:port）。
- serverId 唯一性校验：拉 `listInstances({ namespace })` 与目标环境内已注册实例比对，重复则拦截并提示，不允许进入下一步 / 生成片段。
- 第 2 步「生成接入配置」：按所填值生成两份可一键复制文本——
  - agent `config.yml` 的 `identity` 段（含中文注释，提示 zone 由控制面指派、勿在此配）；
  - run 脚本的 env 片段（`BEACON_AGENT_IDENTITY_NAMESPACE/SERVER_ID/GROUP_HINT/ADDRESS`，FR-41 覆盖）。
  - 复制按钮用既有 `navigator.clipboard` 范式 + 成功 / 失败提示。
- 可选「预建 zone 指派」：在向导内填目标小区（zone），调既有 `PUT /admin/v1/zones/assignments`（`assignZone`）预先把 serverId 指派进 (group, zone)，使新服一上线即归属正确小区、不经「未分配」中间态。
  - 仅 bukkit 角色可预建指派（与后端校验一致：BC 代理不进 zone 指派，FR-8/FR-35/FR-71）。
  - 留空 zone 则跳过指派，仅给出配置片段。
- 范围内：纯前端向导 + 复用既有端点（查重 `listInstances`、指派 `assignZone`、环境下拉 `listNamespaces`）。
- 不做（范围外）：
  - 不新增后端端点（查重与指派均复用既有）。
  - 不自动下发 / 远程安装 agent，不生成完整 `config.yml`（仅 identity 段 + env，连接 / 超时等其它段沿用模板默认）。
  - 不在向导里改 bootstrap-token / endpoints（属部署级一次性配置，模板默认即可）。
  - 不做 zone 之外的覆盖配置预建（配置中心另有入口）。

## 3. 设计（怎么做）

- 纯前端，落点在服务器页（`web/src/pages/ServersPage.tsx`）页眉「添加服务器」按钮，打开新组件 `web/src/pages/servers/AddServerWizard.tsx`（受控 open/onOpenChange）。
- 向导内部两步：步骤切换用本地 state（不引第三方 stepper）。
  - 步骤一表单：namespace 下拉（`namespaceOptions` 复用）、serverId（Input）、role（Select：bukkit/bungee）、group（Combobox 可编辑，候选取页面已有 groupOptions）、address（Input）。
  - 「下一步」前置校验：必填齐全 + serverId 在该 namespace 内不重复。查重数据源为向导内 `useQuery(['instances', { namespace }])` 拉的目标环境实例列表（按 namespace 过滤，避免跨环境误判）。
  - 步骤二：展示两段只读文本（`config.yml` identity 段 / env 段）+ 各自复制按钮；可选 zone 输入（仅 bukkit 显示）+「预建指派」按钮（调 `assignZone`，note 固定为向导来源标注）。
- 片段生成是无副作用纯函数 `web/src/lib/agentOnboarding.ts`：入参 `{ namespace, serverId, group, address }`，出参 `{ configYaml, envScript }`，集中拼接 + 中文注释，便于单测穷举。
- serverId 唯一性判定亦提取为纯函数（或在组件内 useMemo）：`exists = instances.some(i => i.serverId === serverId)`。
- i18n：新增 `servers.wizard*` 文案键；复用 `common.copiedToClipboard` / `common.copyFailed`。
- 不涉及架构决策（无新 ADR）：复用既有端点、纯前端、不碰控制面 / agent / DB；符合「简单优先 / 复用既有」与范围纪律。

## 4. 任务拆分

- [ ] 纯函数 `lib/agentOnboarding.ts`（生成 config.yml identity 段 + env 段）+ 单测
- [ ] `AddServerWizard.tsx` 组件（两步、查重、复制、可选预建指派）+ 组件测试（RTL）
- [ ] `ServersPage.tsx` 接入「添加服务器」按钮
- [ ] i18n 文案
- [ ] 文档同步：PRD 状态（已置开发中）、CHANGELOG 未发布段、本规格

## 5. 验收标准

- 服务器页有「添加服务器」按钮，点击打开向导。
- 填 serverId 与目标环境内已存在实例同名时，被拦截并提示，无法进入下一步。
- 生成的 `config.yml` 片段含所填 namespace/server-id/group-hint/address，且不含 zone（含「zone 由控制面指派」提示）。
- 生成的 env 片段含 `BEACON_AGENT_IDENTITY_NAMESPACE/SERVER_ID/GROUP_HINT/ADDRESS` 四项，值与所填一致。
- 填了 zone 且角色为 bukkit 时，点「预建指派」调 `assignZone({ namespace, serverId, group, zone, note })`。
- bungee 角色不展示 zone 预建（与后端 BC 不进 zone 校验一致）。
- 前端 `pnpm test` + `pnpm build` 绿；新用例覆盖：查重拦截、片段生成内容、预建指派调用、bungee 不显 zone。

## 6. 风险 / 待定

- 查重只挡「当前已在册」实例：同名 serverId 若此刻离线 / 未注册则查不到，向导无法 100% 防撞（真源唯一性仍由控制面注册时把关）。向导查重是早期提示，非权威闸门——与既有改派 / 下线「namespace 取自行」的前端摩擦定位一致。
- 生成片段只覆盖 identity 段，其它段（endpoints/token/timing）沿用模板默认；若部署需改 endpoints/token，运维仍按模板自行调整（向导文案提示）。

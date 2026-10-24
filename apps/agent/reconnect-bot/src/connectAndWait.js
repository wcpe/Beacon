'use strict'

// Beacon E2E 机器人入口。
//
// 职责边界（与 mc-testkit 的分工）：
//   - 公共内核（端口探测 / 重试 / 重连 / action 分发 / 优雅收尾）来自
//     @wcpe/mc-testkit-bot 这个共享包，本包**不复制**它的任何源码（ADR-0014）。
//   - 本包只登记「action → 场景驱动」映射，业务场景放 src/scenarios/。
//
// action 名字必须与 agent-e2e/build.gradle.kts 里 bot { action = ... }
// 以及桩侧场景 id 三处一致，否则会出现"框架起了 bot 但没驱动"的静默空跑。

const { runBot } = require('@wcpe/mc-testkit-bot')

const scenarios = {
  // FR-274：子服关闭后按区回退大厅
  'reconnect-fallback': require('./scenarios/reconnectFallback'),
}

runBot({ scenarios })

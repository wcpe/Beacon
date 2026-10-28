'use strict'

// Beacon FR-282：子服关闭后的回退场景驱动。
//
// 被测行为（见 docs/specs/subserver-close-reconnect-fallback.md）：
//   玩家已在业务子服内 → 该子服关闭 → 代理由 BeaconAgentProxy 接管 ServerKickEvent，
//   把玩家送回「同区大厅」；同区无可用则落「全局登录池」；两级都无则放行原生回退。
//
// 本场景只做驱动与观测，判定交给 Beacon 侧（结果文件由 harness-core 统一收口）：
//   ① 经代理落到 game 后端，等其就绪标记；
//   ② 触发该后端关闭；
//   ③ 观察断线后是否经代理落到大厅（而非被踢回登录界面）；
//   ④ 把落点写进终局标记，供桩侧与 Go 侧断言。

const { waitForMessage } = require('@wcpe/mc-testkit-bot/lib/messages')

/** 与 Beacon 侧 E2E 桩约定：后端就绪标记（带上后端名便于区分落点）。 */
const READY_MARKER = 'BEACON_E2E_READY'
/** 触发当前所在后端关闭（由桩侧执行真实关服）。 */
const TRIGGER_CLOSE_MARKER = 'BEACON_E2E_TRIGGER_CLOSE'
/** 落点上报：携带实际落到的 serverId，供 Go 侧断言"回退了且落在预期层级"。 */
const LANDED_MARKER = 'BEACON_E2E_LANDED'

/**
 * 等后端就绪。
 *
 * @param {Object} bot mineflayer 实例
 * @param {number} timeoutMs 超时
 * @returns {Promise<string>} 就绪所在后端的标识（桩侧在标记里带上自己的 serverId）
 */
async function waitReady(bot, timeoutMs) {
  // waitForMessage 解析出的是归一化后的纯文本，不是消息对象
  const text = await waitForMessage(
    bot,
    (message) => message.includes(READY_MARKER),
    timeoutMs,
    READY_MARKER
  )
  // 标记形如 "BEACON_E2E_READY:game"：冒号后即该后端的角色名
  return text.split(':')[1] || ''
}

module.exports = async function runReconnectFallback(context) {
  const { bot, config, log, reconnectOnDisconnect } = context

  // ① 等落到业务子服（回退场景里它是 game）
  const landedOn = await waitReady(bot, config.readyTimeoutMs)
  log(`已进入后端：${landedOn}`)

  // ② 先登记"断线后重连"，再触发关闭——顺序不能反，
  //    否则关闭瞬间的连接丢失会发生在登记之前，拿不到重连后的新 bot。
  const reconnected = reconnectOnDisconnect()
  bot.chat(TRIGGER_CLOSE_MARKER)
  log('已触发当前后端关闭，等待经代理回退…')

  // ③ 拿到回退后的新 bot
  const survivor = await reconnected
  const landedAfter = await waitReady(survivor, config.readyTimeoutMs)
  log(`已回退并落到：${landedAfter}`)

  // ④ 上报落点。判定（是否同区、是否登录池）由 Beacon 侧桩与 Go 驱动完成，
  //    本驱动不自行下结论，避免"机器人既当被测又当裁判"。
  survivor.chat(`${LANDED_MARKER}:${landedAfter}`)
  log('已上报回退落点')

  // ⑤ 上报后停留片刻：桩侧要在收到消息后写结果文件，
  //    机器人立刻退出会让服务端来不及处理这条聊天事件，表现为"结果文件迟迟不出现"。
  await new Promise((resolve) => setTimeout(resolve, 5000))
  log('停留结束，场景驱动完成')
}

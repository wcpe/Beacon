/**
 * 工作台拓印审核浮层的示意 diff 数据（FR-111）。
 *
 * 左→右下发的「拓印单人自审门」浮层（ImprintReviewOverlay / BatchReviewOverlay / PublishPanel 批量 diff）
 * 展示「期望合并值 ⟷ 服务器现状」。真链路下该 diff 由 FR-46 拓印端点（imprintDiff）按 commandId 拉取，
 * 属左→右下发写流程（spec §6 标 partial，端到端在真机维度验证）；此处保留少量示意 diff，
 * 让 dev 与组件测试下浮层有内容可渲染。**非 mock API 端点数据**，仅前端示意常量。
 */

import type { ImprintDiff } from './types'

// 按文件名（末段）索引拓印 diff 示意
export const imprintDiffs: Record<string, ImprintDiff> = {
  'motd.yml': {
    expected: `# 服务器 MOTD（受管·全局 期望值）
motd: '&b&l欢迎来到大厅 &7| &f输入 /help 查看帮助'
max-players: 200
`,
    current: `# 服务器 MOTD（服务器现状）
motd: '&b欢迎'
max-players: 100
`,
  },
  'spawn.yml': {
    expected: `# 出生点配置（受管·组 main 期望值）
spawns:
  default: { world: world, x: 128.5, y: 64.0, z: -64.5 }
  pvp: { world: pvp, x: 0.0, y: 72.0, z: 0.0 }
respawn-at-spawn: true
`,
    current: `# 出生点配置（服务器现状）
spawns:
  default: { world: world, x: 120.0, y: 64.0, z: -64.5 }
respawn-at-spawn: false
`,
  },
}

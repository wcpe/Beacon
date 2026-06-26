// StatusTile 单测：角色图标（bukkit=Server / bungee=Router）+ 健康色（按 status 变色）+ 关键指标（子服 TPS·人数 / BC 连接·后端）。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import StatusTile from './StatusTile'
import type { InstanceView } from '@/api/types'

// 最小实例桩工厂（状态墙仅读 role/status/tps/playerCount/proxy.*）。
function inst(overrides: Partial<InstanceView>): InstanceView {
  return {
    namespace: 'prod',
    serverId: 'srv-1',
    role: 'bukkit',
    group: '',
    zone: null,
    assigned: false,
    address: '',
    version: '',
    agentVersion: '',
    status: 'online',
    capacity: 0,
    weight: 0,
    metadata: {},
    lastHeartbeat: '',
    lastHeartbeatAgeSec: 0,
    healthReason: '',
    appliedMd5: '',
    playerCount: 0,
    tps: 0,
    backends: [],
    zoneDefaultEntry: false,
    proxy: { onlineConnections: 0, threadCount: 0, uptimeMs: 0, backendUp: 0, backendTotal: 0, backendAvgLatencyMs: -1 },
    registeredAt: '',
    ...overrides,
  }
}

describe('StatusTile', () => {
  it('子服（bukkit）：展示 serverId + TPS·人数关键指标', () => {
    render(<StatusTile instance={inst({ serverId: 'lobby-1', role: 'bukkit', tps: 19.8, playerCount: 42 })} />)
    expect(screen.getByText('lobby-1')).toBeInTheDocument()
    expect(screen.getByText('TPS')).toBeInTheDocument()
    expect(screen.getByText('19.8')).toBeInTheDocument()
    expect(screen.getByText('人数')).toBeInTheDocument()
    expect(screen.getByText('42')).toBeInTheDocument()
  })

  it('BC 代理（bungee）：展示连接数·后端可达（up/total）关键指标', () => {
    render(
      <StatusTile
        instance={inst({
          serverId: 'proxy-1',
          role: 'bungee',
          proxy: { onlineConnections: 150, threadCount: 48, uptimeMs: 0, backendUp: 3, backendTotal: 4, backendAvgLatencyMs: 12 },
        })}
      />,
    )
    expect(screen.getByText('proxy-1')).toBeInTheDocument()
    expect(screen.getByText('连接')).toBeInTheDocument()
    expect(screen.getByText('150')).toBeInTheDocument()
    expect(screen.getByText('后端')).toBeInTheDocument()
    expect(screen.getByText('3 / 4')).toBeInTheDocument()
  })

  it('在线状态用绿色健康色条（bg-green-600）', () => {
    const { container } = render(<StatusTile instance={inst({ status: 'online' })} />)
    // 左侧健康色条按等级上色：online → green
    expect(container.querySelector('.bg-green-600')).not.toBeNull()
    expect(container.querySelector('.bg-red-600')).toBeNull()
  })

  it('失联状态用红色健康色条（bg-red-600）', () => {
    const { container } = render(<StatusTile instance={inst({ status: 'lost' })} />)
    expect(container.querySelector('.bg-red-600')).not.toBeNull()
    expect(container.querySelector('.bg-green-600')).toBeNull()
  })

  it('亚健康状态用琥珀色健康色条（bg-amber-500）', () => {
    const { container } = render(<StatusTile instance={inst({ status: 'degraded' })} />)
    expect(container.querySelector('.bg-amber-500')).not.toBeNull()
  })

  it('状态文案经 i18n 显中文（online→在线、degraded→亚健康）', () => {
    const { rerender } = render(<StatusTile instance={inst({ status: 'online' })} />)
    expect(screen.getByText('在线')).toBeInTheDocument()
    // degraded 此前缺 i18n 键，会回退英文原值；补键后须显「亚健康」
    rerender(<StatusTile instance={inst({ status: 'degraded' })} />)
    expect(screen.getByText('亚健康')).toBeInTheDocument()
  })

  it('数值字段缺省时按 0 兜底不崩溃（容错）', () => {
    // 故意造缺 tps/playerCount 的越界桩：应渲染 0 而非抛错。
    const broken = { serverId: 'srv-x', role: 'bukkit', status: 'online', proxy: {} } as unknown as InstanceView
    render(<StatusTile instance={broken} />)
    expect(screen.getByText('srv-x')).toBeInTheDocument()
    expect(screen.getByText('0.0')).toBeInTheDocument()
  })
})

// ReassignDialog 单测（FR-71）：高摩擦显式改派——
// ① 手输 serverId 与卡片不符时「确认改派」禁用；相符且选齐目标区时启用；
// ② 提交以正确入参（namespace/serverId/目标 group/zone/备注）回调 onConfirm。
import { describe, it, expect, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { InstanceView } from '../../api/types'
import ReassignDialog from './ReassignDialog'

function inst(overrides: Partial<InstanceView>): InstanceView {
  return {
    namespace: 'prod',
    serverId: 'lobby-1',
    role: 'bukkit',
    group: 'gA',
    zone: 'z1',
    assigned: true,
    address: '',
    version: '',
    status: 'online',
    capacity: 0,
    weight: 0,
    metadata: {},
    lastHeartbeat: '',
    appliedMd5: '',
    playerCount: 0,
    tps: 0,
    backends: [],
    zoneDefaultEntry: false,
    proxy: {
      onlineConnections: 0,
      threadCount: 0,
      uptimeMs: 0,
      backendUp: 0,
      backendTotal: 0,
      backendAvgLatencyMs: -1,
    },
    registeredAt: '',
    ...overrides,
  }
}

function renderDialog(onConfirm = vi.fn(), currentNote = '原备注') {
  const instance = inst({})
  render(
    <ReassignDialog
      open
      onOpenChange={() => {}}
      instance={instance}
      currentNote={currentNote}
      groupOptions={['gA', 'gB']}
      zoneOptions={['z1', 'z2']}
      pending={false}
      onConfirm={onConfirm}
    />,
  )
  return { instance, onConfirm }
}

async function pick(label: string, value: string) {
  await userEvent.click(screen.getByLabelText(label))
  const listbox = await screen.findByRole('listbox')
  await userEvent.click(within(listbox).getByText(value))
}

describe('ReassignDialog 手输确认（FR-71）', () => {
  it('手输 serverId 不符时「确认改派」禁用', async () => {
    renderDialog()
    await pick('大区', 'gA')
    await pick('小区', 'z2')
    await userEvent.type(screen.getByLabelText('手输 serverId 确认'), 'lobby-9')
    expect(screen.getByRole('button', { name: '确认改派' })).toBeDisabled()
  })

  it('未选齐目标区时「确认改派」禁用（即便 serverId 相符）', async () => {
    renderDialog()
    await userEvent.type(screen.getByLabelText('手输 serverId 确认'), 'lobby-1')
    expect(screen.getByRole('button', { name: '确认改派' })).toBeDisabled()
  })

  it('serverId 相符且选齐目标区时可提交，并以正确入参调 onConfirm', async () => {
    const { onConfirm } = renderDialog()
    await pick('大区', 'gA')
    await pick('小区', 'z2')
    await userEvent.type(screen.getByLabelText('手输 serverId 确认'), 'lobby-1')
    const btn = screen.getByRole('button', { name: '确认改派' })
    expect(btn).toBeEnabled()
    await userEvent.click(btn)
    expect(onConfirm).toHaveBeenCalledWith({
      namespace: 'prod',
      serverId: 'lobby-1',
      group: 'gA',
      zone: 'z2',
      note: '原备注',
    })
  })
})

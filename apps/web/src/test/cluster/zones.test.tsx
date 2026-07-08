// /zones 区服分配页测试（主从：树 + 非模态右侧窄栏）：结构树常规渲染（含代理角色标注）、空态引导、
// 勾选批量分配写闭环、可搜索树目标选择器、拖拽落区（原生 HTML5）。未分配收敛为窄栏入口，故分配用例先开栏。
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import ZonesPage from '../../pages/zones'
import { ASSIGN_DRAG_MIME } from '../../features/cluster/assign-drag'
import { createTestServer, renderPage, useScenario } from './harness'

// 构造一个可读写的 dataTransfer 桩（jsdom 无原生 DataTransfer）：
// 由组件的 dragStart 写入真实载荷，dragOver/drop 复用同一实例读取，故无需在测试里硬编码行 id。
function makeDragDataTransfer(): DataTransfer {
  const store: Record<string, string> = {}
  return {
    setData: (type: string, value: string) => {
      store[type] = value
    },
    getData: (type: string) => store[type] ?? '',
    effectAllowed: 'move',
    dropEffect: 'move',
    types: [ASSIGN_DRAG_MIME],
  } as unknown as DataTransfer
}

// 本文件独享 mock 服务端实例
const server = createTestServer()

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  server.resetHandlers()
})
afterAll(() => {
  server.close()
})

describe('/zones 区服分配页', () => {
  it('常规态渲染结构树（集群 + 大区 + 小区）', async () => {
    useScenario('normal')
    renderPage(<ZonesPage />)

    // 结构树出现已知集群、大区与小区（集群 + 大区默认展开）
    expect(await screen.findByText('bc-main')).toBeInTheDocument()
    expect(await screen.findByText('华东大区')).toBeInTheDocument()
    expect(await screen.findByText('area-1')).toBeInTheDocument()
  })

  it('集群节点标注代理角色计数', async () => {
    useScenario('normal')
    renderPage(<ZonesPage />)

    // 集群头带「代理 · N」角色徽标（代理服明确标注）
    expect(await screen.findByText(/代理 · \d/)).toBeInTheDocument()
  })

  it('空态给出建集群引导', async () => {
    useScenario('empty')
    renderPage(<ZonesPage />)

    expect(
      await screen.findByText('尚未建立任何 BC 集群，先新建一个集群开始规划区服'),
    ).toBeInTheDocument()
  })

  it('打开未分配窄栏勾选后经可搜索树选目标批量分配，该 server 从窄栏消失（写闭环 + 树选择器）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ZonesPage />)

    // 等结构树加载，再从顶部入口打开未分配窄栏
    await screen.findByText('bc-main')
    await user.click(await screen.findByRole('button', { name: /未分配/ }))

    // 窄栏内勾选 build-1（chip 内的 checkbox）
    const chip = (await screen.findByText('build-1')).closest('div')
    expect(chip).not.toBeNull()
    await user.click(within(chip as HTMLElement).getByRole('checkbox'))

    // 打开「分配到…」目标选择器
    await user.click(screen.getByRole('button', { name: '分配到…' }))

    // 目标选择器为可搜索树：搜索 area 过滤，再点小区叶 area-1
    const dialog = await screen.findByRole('dialog')
    const search = within(dialog).getByLabelText('搜索目标（按名称过滤）')
    await user.type(search, 'area-1')
    await user.click(await within(dialog).findByRole('treeitem', { name: /area-1/ }))
    await user.click(within(dialog).getByRole('button', { name: '确认分配' }))

    // build-1 从未分配窄栏消失
    await waitFor(() => {
      expect(screen.queryByText('build-1')).not.toBeInTheDocument()
    })
  })

  it('目标选择器树搜索按名称过滤，命中项可见、非命中项隐藏', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ZonesPage />)

    await screen.findByText('bc-main')
    await user.click(await screen.findByRole('button', { name: /未分配/ }))
    const chip = (await screen.findByText('build-1')).closest('div')
    await user.click(within(chip as HTMLElement).getByRole('checkbox'))
    await user.click(screen.getByRole('button', { name: '分配到…' }))

    const dialog = await screen.findByRole('dialog')
    const search = within(dialog).getByLabelText('搜索目标（按名称过滤）')
    // 搜索一个不存在的名称 → 空匹配提示
    await user.type(search, 'zzz-none')
    expect(await within(dialog).findByText('无匹配的目标节点')).toBeInTheDocument()
    // 改搜 area-1 → 命中叶出现
    await user.clear(search)
    await user.type(search, 'area-1')
    expect(await within(dialog).findByRole('treeitem', { name: /area-1/ })).toBeInTheDocument()
  })

  it('从窄栏拖拽 build-1 落到小区 area-1 完成分配（原生 HTML5 拖拽落区）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ZonesPage />)

    await screen.findByText('bc-main')
    await user.click(await screen.findByRole('button', { name: /未分配/ }))

    // 定位窄栏里的 build-1 chip（可拖起）
    const chip = (await screen.findByText('build-1')).closest('[draggable="true"]')
    expect(chip).not.toBeNull()

    // 定位小区 area-1 的树行（放置目标）
    const zoneRow = (await screen.findByText('area-1')).closest('[role="button"]')
    expect(zoneRow).not.toBeNull()

    // 原生拖拽序列：dragStart（组件写真实载荷）→ dragOver（目标 preventDefault 接收）→ drop（触发分配）
    const dt = makeDragDataTransfer()
    fireEvent.dragStart(chip as HTMLElement, { dataTransfer: dt })
    fireEvent.dragOver(zoneRow as HTMLElement, { dataTransfer: dt })
    fireEvent.drop(zoneRow as HTMLElement, { dataTransfer: dt })

    // 分配成功后 build-1 从未分配窄栏消失（写闭环失效缓存）
    await waitFor(() => {
      expect(screen.queryByText('build-1')).not.toBeInTheDocument()
    })
  })
})

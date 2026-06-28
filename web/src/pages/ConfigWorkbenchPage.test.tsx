// ConfigWorkbenchPage 关键流程交互测试（FR-115）：
// 把工作台数据 hook（useWorkbenchData 全套）与 CodeEditor / useMessage 替身化，
// 在 PageHeaderProvider + MemoryRouter 下渲染整页，覆盖关键闭环：
//  ① 选中受管文件 → 顶部状态栏发布 → PublishPanel 打开 → 确认发布 → 队列加「下发·已完成」行 + 操作日志记 publish；
//  ② 选中服务器文件 → 抓取 → 二次确认 → 入队（待审 ingest）+ 操作日志记 fetch；
//  ③ 队列待审行点开 → ingest / imprint 审核浮层打开 → 确认转完成；
//  ④ 撤回（逐条 / 批量）：操作日志撤回标记已撤回；
//  ⑤ 生效预览 diff 计数（切到「生效预览」视图，断言「共 X 处覆盖」「N 处定制」）；
//  ⑥ 文件夹拖拽语义已在 PanelTree 单测覆盖，这里覆盖右键菜单触发「移动」确认入口；
//  ⑦ 编辑器：双击文件 → 浮层多标签 / 历史 / 保存确认（toast）。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Routes, Route, useLocation } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { PageHeaderProvider } from '@/components/PageHeader'
import type { ReactElement } from 'react'

// ---- 替身：Monaco 编辑器 / toast 消息 ----
vi.mock('@/components/CodeEditor', () => ({
  default: (props: { value?: string; onChange?: (v: string) => void }) => (
    <textarea data-testid="code-editor" value={props.value ?? ''} onChange={(e) => props.onChange?.(e.target.value)} />
  ),
}))

const toastSuccess = vi.fn()
vi.mock('@/components/useMessage', () => ({
  useMessage: () => ({ showSuccess: toastSuccess, showError: vi.fn() }),
}))

// ---- 替身：工作台数据 hook（注入受控 mock 数据，规避 fetch/MSW）----
vi.mock('./configs-workbench/useWorkbenchData', () => ({
  useManagedTree: vi.fn(),
  useServerTree: vi.fn(),
  useSyncQueue: vi.fn(),
  useOperationLog: vi.fn(),
  useWorkbenchOptions: vi.fn(),
  useWorkbenchFile: vi.fn(),
  useIngestScanList: vi.fn(),
  useEffectivePreview: vi.fn(),
  usePublishImpact: vi.fn(),
}))

import ConfigWorkbenchPage from './ConfigWorkbenchPage'
import * as wb from './configs-workbench/useWorkbenchData'
import type {
  ManagedNode,
  ServerNode,
  SyncQueueRow,
  OpLogEntry,
  EffectiveFile,
  PublishImpact,
  WorkbenchFile,
} from './configs-workbench/types'

// ---- 受控 mock 数据 ----
const MANAGED: ManagedNode[] = [
  {
    key: 'plugins',
    name: 'plugins',
    type: 'folder',
    sync: 'drift',
    children: [
      { key: 'plugins/spawn.yml', name: 'spawn.yml', type: 'file', sync: 'drift', scope: 'group', version: 4, modifiedAt: '今天' },
      { key: 'plugins/motd.yml', name: 'motd.yml', type: 'file', sync: 'synced', scope: 'global', version: 2, modifiedAt: '3 天前' },
    ],
  },
]
const SERVER: ServerNode[] = [
  {
    key: 'srv/plugins',
    name: 'plugins',
    type: 'folder',
    mark: 'drift',
    children: [
      { key: 'srv/plugins/regions.yml', name: 'regions.yml', type: 'file', mark: 'untracked', size: '88 KB', fileType: 'YAML', modifiedAt: '今天' },
    ],
  },
]
const QUEUE_SEED: SyncQueueRow[] = [
  { id: 'q-ing', name: 'WorldGuard/regions.yml', direction: 'fetch', status: 'pending-ingest', scopeTarget: '组 main', sourcePath: 'a', targetPath: 'b', time: '14:33' },
  { id: 'q-imp', name: 'motd.yml', direction: 'push', status: 'pending-imprint', scopeTarget: '实例 lobby-1', sourcePath: 'c', targetPath: 'd', time: '14:30' },
]
const LOG_SEED: OpLogEntry[] = [
  { id: 'log-seed-1', time: '14:33', action: 'push', operator: 'admin', files: ['spawn.yml'], target: '实例 lobby-1', detail: '下发 spawn.yml', undone: false },
]
const OPTIONS = {
  scopes: [
    { value: 'global', label: '全局', scope: 'global' as const },
    { value: 'group:main', label: '组 main', scope: 'group' as const },
  ],
  servers: [{ serverId: 'lobby-1', label: 'lobby-1', online: true }],
}
const EFFECTIVE: EffectiveFile[] = [
  {
    name: 'spawn.yml',
    keys: [
      { key: 'world', chain: [{ scope: 'global', value: 'world' }] },
      { key: 'x', chain: [{ scope: 'global', value: '120' }, { scope: 'group', value: '128' }] },
    ],
  },
]
const PUBLISH_IMPACT: PublishImpact = {
  files: [{ name: 'spawn.yml', scope: 'group', fromVersion: 4, toVersion: 5 }],
  groups: [{ scope: 'group', label: '组 main', servers: [{ serverId: 'lobby-1', online: true, changed: true }] }],
  driftCount: 1,
}
const FILE: WorkbenchFile = {
  key: 'plugins/spawn.yml',
  fileId: 7,
  namespace: 'prod',
  group: 'main',
  dataId: 'spawn.yml',
  scope: 'group',
  targetServer: 'lobby-1',
  format: 'yaml',
  content: 'a: 1\n',
  revisions: [
    { version: 4, author: 'admin', time: '今天', comment: '新增', content: 'a: 1\n' },
    { version: 3, author: 'ops', time: '昨天', comment: '初版', content: 'a: 0\n' },
  ],
}

// 默认查询替身：data + 标志位
function q<T>(data: T, over: Record<string, unknown> = {}) {
  return { data, isLoading: false, refetch: vi.fn(), ...over } as never
}

function installDefaults() {
  vi.mocked(wb.useManagedTree).mockReturnValue(q(MANAGED))
  vi.mocked(wb.useServerTree).mockReturnValue(q(SERVER))
  vi.mocked(wb.useSyncQueue).mockReturnValue(q(QUEUE_SEED))
  vi.mocked(wb.useOperationLog).mockReturnValue(q(LOG_SEED))
  vi.mocked(wb.useWorkbenchOptions).mockReturnValue(q(OPTIONS))
  vi.mocked(wb.useWorkbenchFile).mockReturnValue(q(FILE))
  vi.mocked(wb.useIngestScanList).mockReturnValue(
    q({ items: [{ path: 'regions.yml', size: '88 KB', ignored: false, defaultPick: true }], ignoreRules: ['*.db'] }),
  )
  vi.mocked(wb.useEffectivePreview).mockReturnValue(q(EFFECTIVE))
  vi.mocked(wb.usePublishImpact).mockReturnValue(q(PUBLISH_IMPACT))
}

// 暴露当前路径供断言双击导航到真详情路由（FR-112）
function LocationProbe() {
  const loc = useLocation()
  return <div data-testid="location">{loc.pathname}</div>
}

function renderPage(ui: ReactElement = <ConfigWorkbenchPage />, path = '/configs') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <PageHeaderProvider>
          <LocationProbe />
          <Routes>
            <Route path="/configs" element={ui} />
            {/* FR-112：真详情路由由 ConfigEditorPage 承载；此处仅占位以断言导航发生 */}
            <Route path="/configs/:id" element={<div>编辑器路由</div>} />
          </Routes>
        </PageHeaderProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('ConfigWorkbenchPage 关键流程（FR-115）', () => {
  beforeEach(() => {
    toastSuccess.mockClear()
    vi.clearAllMocks()
    installDefaults()
  })

  it('渲染双面板：受管/服务器标题 + 文件', () => {
    renderPage()
    expect(screen.getByText('受管配置')).toBeInTheDocument()
    expect(screen.getByText('服务器')).toBeInTheDocument()
    expect(screen.getByText('spawn.yml')).toBeInTheDocument()
    expect(screen.getByText('regions.yml')).toBeInTheDocument()
  })

  // fix-B 回归护栏：窄屏面板不再被挤成竖排——面板标题截断省略、双面板行可横向滚动（jsdom 无布局引擎，仅锁类名）
  it('面板标题截断（truncate）、双面板行窄屏横向滚动（overflow-x-auto）', () => {
    renderPage()
    const title = screen.getByText('受管配置')
    expect(title.className).toMatch(/truncate/)
    // 双面板行为横向滚动容器
    expect(title.closest('.overflow-x-auto')).not.toBeNull()
  })

  it('① 选中受管文件 → 发布面板 → 确认发布：toast 含已发布 + 队列出现「按覆盖层热推」完成行', async () => {
    renderPage()
    // 勾选 spawn.yml（受管侧复选框）
    fireEvent.click(screen.getByRole('checkbox', { name: 'spawn.yml' }))
    // 顶部状态栏出现「发布选中 1 项」
    const publishBtn = await screen.findByRole('button', { name: '发布选中 1 项' })
    await userEvent.click(publishBtn)
    // PublishPanel 打开
    expect(await screen.findByText('发布 + 影响面')).toBeInTheDocument()
    // driftCount=1 → 勾审阅闸再发布
    await userEvent.click(screen.getByLabelText('我已审阅全部 diff'))
    await userEvent.click(screen.getByRole('button', { name: '发布并热推（1 台）' }))
    // 发布 toast
    await waitFor(() =>
      expect(toastSuccess).toHaveBeenCalledWith(expect.stringContaining('已发布 1 项')),
    )
    // 队列里出现发布行（覆盖层·目标列文案「按覆盖层热推」）
    expect(screen.getByText('按覆盖层热推')).toBeInTheDocument()
  })

  it('② 选中服务器文件 → 抓取 → 二次确认 → 入队 + fetch toast', async () => {
    renderPage()
    fireEvent.click(screen.getByRole('checkbox', { name: 'regions.yml' }))
    const fetchBtn = await screen.findByRole('button', { name: /抓取选中 1 项/ })
    await userEvent.click(fetchBtn)
    // 二次确认弹窗
    expect(await screen.findByText('抓取 1 项到受管？')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '确认抓取' }))
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(expect.stringContaining('已加入抓取队列')))
  })

  it('③ 队列待审 ingest 行点开 → ingest 审核浮层 → 确认转完成', async () => {
    renderPage()
    // 队列 tab 默认显示；点开 fetch 待审行（名字 WorldGuard/regions.yml）
    await userEvent.click(screen.getByText('WorldGuard/regions.yml'))
    expect(await screen.findByText('反向抓取 · 审核纳管清单')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /确认纳管/ }))
    // 浮层关闭
    await waitFor(() => expect(screen.queryByText('反向抓取 · 审核纳管清单')).not.toBeInTheDocument())
  })

  it('③b 队列待审 imprint 行点开 → 拓印审核浮层（含审阅闸）→ 确认', async () => {
    renderPage()
    // motd.yml 在受管树与队列里都有；点队列里那条（带「点击打开审核浮层」title 的可点行）
    const queueRow = screen
      .getAllByText('motd.yml')
      .map((el) => el.closest('[title="点击打开审核浮层"]'))
      .find(Boolean) as HTMLElement
    await userEvent.click(queueRow)
    expect(await screen.findByText('拓印审核 · 期望值 ⟷ 服务器现状')).toBeInTheDocument()
    // 审阅闸：勾选后确认
    await userEvent.click(screen.getByLabelText('我已审阅此 diff'))
    await userEvent.click(screen.getByRole('button', { name: '确认下发' }))
    await waitFor(() => expect(screen.queryByText('拓印审核 · 期望值 ⟷ 服务器现状')).not.toBeInTheDocument())
  })

  it('④ 逐条撤回：操作日志中种子 push 项撤回 → 标记「已撤回」 + toast', async () => {
    renderPage()
    // 切到操作日志 tab
    await userEvent.click(screen.getByRole('button', { name: /操作日志/ }))
    expect(screen.getByText('下发 spawn.yml')).toBeInTheDocument()
    // 逐条撤回（行内「撤回」钮；状态栏的是「撤回上一步」，用精确名区分）
    await userEvent.click(screen.getByRole('button', { name: '撤回' }))
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(expect.stringContaining('已撤回')))
    expect(screen.getByText('已撤回')).toBeInTheDocument()
  })

  it('④b 批量撤回：勾选日志项 → 批量撤回 → toast', async () => {
    renderPage()
    await userEvent.click(screen.getByRole('button', { name: /操作日志/ }))
    // 勾选未撤回项复选框
    await userEvent.click(screen.getByRole('checkbox', { name: '下发 spawn.yml' }))
    await userEvent.click(screen.getByRole('button', { name: /批量撤回 1 项/ }))
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(expect.stringContaining('已撤回')))
  })

  it('⑤ 生效预览：切到「生效预览」视图，断言覆盖面计数与定制计数', async () => {
    renderPage()
    await userEvent.click(screen.getByRole('button', { name: '生效预览' }))
    // 总览：1 处覆盖 · 1/1 文件
    expect(await screen.findByText('共 1 处覆盖 · 1/1 文件')).toBeInTheDocument()
    // 文件级：1 处定制
    expect(screen.getByText('1 处定制')).toBeInTheDocument()
  })

  it('⑥ 右键受管文件 → 菜单 → 重命名 → 二次确认入口打开', async () => {
    renderPage()
    fireEvent.contextMenu(screen.getByText('spawn.yml'))
    expect(await screen.findByText('重命名')).toBeInTheDocument()
    await userEvent.click(screen.getByText('重命名'))
    expect(await screen.findByText('重命名「spawn.yml」？')).toBeInTheDocument()
  })

  it('⑦ 双击受管文件 → 导航到真详情编辑器路由 /configs/:id（FR-112）', async () => {
    renderPage()
    fireEvent.doubleClick(screen.getByText('spawn.yml'))
    // 双击进真详情子路由（key=plugins/spawn.yml，encodeURIComponent）
    await waitFor(() =>
      expect(screen.getByTestId('location').textContent).toBe('/configs/plugins%2Fspawn.yml'),
    )
  })

  it('⑦b 右键「编辑」受管文件 → 页内编辑器浮层（历史 + 保存确认 toast）', async () => {
    renderPage()
    // 右键菜单的「编辑」走页内浮层（保留的快捷编辑入口，区别于双击进真路由）
    fireEvent.contextMenu(screen.getByText('spawn.yml'))
    await userEvent.click(await screen.findByText('编辑'))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('历史修订')).toBeInTheDocument()
    expect(within(dialog).getByText('v4')).toBeInTheDocument()
    expect(within(dialog).getByText('当前')).toBeInTheDocument()
    // 保存确认 toast（浮层仍为原型示意保存）
    await userEvent.click(within(dialog).getByRole('button', { name: '保存' }))
    expect(toastSuccess).toHaveBeenCalledWith('已保存（原型示意）')
  })
})

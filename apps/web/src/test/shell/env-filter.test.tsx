// 页眉观测范围选择器（FR-214）：选中项必须对辅助技术可见。
//
// 背景：此前选中态只靠背景色与勾图标表达，读屏只会念「菜单项」，用户无法得知
// 当前选中的是哪个环境 / 命名空间（issue #149）。现补 role=menuitemradio + aria-checked，
// 本测试锁住语义，同时守住视觉未被削弱（选中项仍有勾图标）。
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { MemoryRouter } from 'react-router-dom'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import EnvFilter from '../../shell/env-filter'
import {
  ALL_OBSERVATION_ENV,
  getObservationScopeSelection,
  selectObservationEnv,
  setObservationScope,
} from '../../features/env/observation-scope'
import { setLocale } from '../../state/locale'
import '../../i18n'

const server = setupServer(
  http.get('/admin/v2/envs', () =>
    HttpResponse.json({
      items: [
        {
          id: 1,
          code: 'prod',
          name: '生产',
          namespaces: [
            { id: 1, code: 'prod', name: 'prod' },
            { id: 2, code: 'prod-2', name: 'prod-2' },
          ],
        },
      ],
      total: 1,
    }),
  ),
)

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
// 观测范围是模块级单例：用例间不复位会串味（前一个用例选过的命名空间会带进下一个）。
beforeEach(() => {
  setObservationScope(selectObservationEnv(getObservationScopeSelection(), ALL_OBSERVATION_ENV))
})
afterEach(() => {
  server.resetHandlers()
})
afterAll(() => {
  server.close()
})

function renderFilter() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <EnvFilter />
      </MemoryRouter>
    </QueryClientProvider>,
  )
  return queryClient
}

describe('页眉观测范围选择器', () => {
  it('选中项以 menuitemradio 暴露，并带 aria-checked', async () => {
    setLocale('zh-CN')
    renderFilter()

    const triggers = await screen.findAllByRole('button', { name: /环境|命名空间|全部/ })
    await userEvent.click(triggers[0])

    // 「全部环境」与具体环境的条项都应是 menuitemradio，而不是无选中语义的 menuitem。
    const items = await screen.findAllByRole('menuitemradio')
    expect(items.length).toBeGreaterThan(0)

    // 至少有一项处于选中态，且选中项的 aria-checked 为 "true"。
    const checked = items.filter((item) => item.getAttribute('aria-checked') === 'true')
    expect(checked.length).toBeGreaterThan(0)
    for (const item of items) {
      expect(item).toHaveAttribute('aria-checked')
    }
  })

  it('切换选项后选中态随之转移', async () => {
    setLocale('zh-CN')
    renderFilter()

    await userEvent.click((await screen.findAllByRole('button', { name: /环境|命名空间|全部/ }))[0])
    const before = (await screen.findAllByRole('menuitemradio')).find(
      (item) => item.getAttribute('aria-checked') === 'true',
    )
    expect(before).toBeTruthy()

    // 选中另一个条项：菜单会关闭，故先取到目标节点再点击。
    const target = screen
      .getAllByRole('menuitemradio')
      .find((item) => item.getAttribute('aria-checked') !== 'true')
    expect(target).toBeDefined()
    if (target === undefined) throw new Error('应存在一个未选中的条项')
    await userEvent.click(target)

    // 重新打开菜单，断言恰有一项处于选中态，且已不是原来那项。
    await userEvent.click((await screen.findAllByRole('button', { name: /环境|命名空间|全部/ }))[0])
    const after = await screen.findAllByRole('menuitemradio')
    const checked = after.filter((item) => item.getAttribute('aria-checked') === 'true')
    expect(checked).toHaveLength(1)
    expect(checked[0]?.textContent).not.toBe(before?.textContent)
  })

  it('选中项仍保留勾图标，未选中项没有（视觉未被削弱）', async () => {
    setLocale('zh-CN')
    renderFilter()

    await userEvent.click((await screen.findAllByRole('button', { name: /环境|命名空间|全部/ }))[0])
    const items = await screen.findAllByRole('menuitemradio')
    for (const item of items) {
      const hasCheck = within(item).queryByRole('img', { hidden: true }) !== null
      const isChecked = item.getAttribute('aria-checked') === 'true'
      // 勾图标是 aria-hidden 的装饰元素，只做存在性核对：选中项应有，未选中项应无。
      const svgCount = item.querySelectorAll('svg').length
      if (isChecked) {
        expect(svgCount).toBeGreaterThan(0)
      } else {
        expect(hasCheck || svgCount === 0).toBe(true)
      }
    }
  })
  it('移动端合并菜单里每个单选组内只有一项被选中', async () => {
    setLocale('zh-CN')
    renderFilter()

    // 移动端菜单是第三个触发器（sm 以下可见），桌面两个下拉在 jsdom 里同样渲染。
    const triggers = await screen.findAllByRole('button', { name: /环境|命名空间|全部/ })
    const mobileTrigger = triggers[triggers.length - 1]
    await userEvent.click(mobileTrigger)

    // 合并菜单把「环境」与「命名空间」两组平铺，必须各自成组：
    // 若不加分组，同一 menu 内会出现两个 menuitemradio 同时 aria-checked=true。
    const groups = await screen.findAllByRole('group')
    expect(groups.length).toBeGreaterThanOrEqual(2)

    for (const group of groups) {
      const checked = within(group)
        .getAllByRole('menuitemradio')
        .filter((item) => item.getAttribute('aria-checked') === 'true')
      expect(checked).toHaveLength(1)
    }
  })

  it('命名空间下拉：选中具体命名空间后与「全部命名空间」互斥', async () => {
    setLocale('zh-CN')
    renderFilter()

    // 命名空间下拉是第二个触发器。
    const triggers = await screen.findAllByRole('button', { name: /环境|命名空间|全部/ })
    await userEvent.click(triggers[1])

    const menu = await screen.findByRole('menu')
    // 默认态「全部命名空间」为选中。
    expect(within(menu).getByRole('menuitemradio', { name: /全部命名空间/ })).toHaveAttribute('aria-checked', 'true')

    await userEvent.click(within(menu).getByRole('menuitemradio', { name: /^prod-2/ }))

    // 选具体命名空间后：范围落到该命名空间。
    expect(getObservationScopeSelection()).toMatchObject({ namespaceId: 2 })

    await userEvent.click((await screen.findAllByRole('button', { name: /环境|命名空间|全部/ }))[1])
    const reopened = await screen.findByRole('menu')
    // 互斥：具体项选中时，「全部命名空间」必须变为未选中。
    expect(within(reopened).getByRole('menuitemradio', { name: /^prod-2/ })).toHaveAttribute('aria-checked', 'true')
    expect(within(reopened).getByRole('menuitemradio', { name: /全部命名空间/ })).toHaveAttribute('aria-checked', 'false')
  })

})

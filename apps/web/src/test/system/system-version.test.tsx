// /system/version 版本与更新页测试：常规渲染、空态（无更新）、触发更新写闭环、代理测试。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'
import { http, HttpResponse } from 'msw'

import SystemVersionPage from '../../pages/system-version'
import { createTestServer, renderPage, useScenario } from './harness'

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

describe('/system/version 版本与更新页', () => {
  it('常规态渲染当前版本与可用更新', async () => {
    useScenario('normal')
    renderPage(<SystemVersionPage />)

    // 当前版本 v0.21.0
    expect(await screen.findByText('v0.21.0')).toBeInTheDocument()
    // normal 场景有可用更新 v0.22.0（版本徽标 / 更新说明标题多处出现）
    await waitFor(() => {
      expect(screen.getAllByText(/v0\.22\.0/).length).toBeGreaterThan(0)
    })
    // 紧凑分区：版本信息 / 更新与渠道 / 维护操作三段常驻
    expect(screen.getByText('版本信息')).toBeInTheDocument()
    expect(screen.getByText('更新与渠道')).toBeInTheDocument()
    expect(screen.getByText('维护操作')).toBeInTheDocument()
  })

  it('空态（已是最新）给出已最新提示', async () => {
    useScenario('empty')
    renderPage(<SystemVersionPage />)

    expect(await screen.findByText('已是最新版本')).toBeInTheDocument()
  })

  it('检查失败时显示降级状态、保留手动检查并禁用应用更新', async () => {
    useScenario('normal')
    const forceValues: string[] = []
    server.use(
      http.get('/admin/v1/system/update-check', ({ request }) => {
        forceValues.push(new URL(request.url).searchParams.get('force') ?? '')
        return HttpResponse.json({
          status: 'check-failed',
          failureReason: '查 release 列表失败: proxyconnect tcp 10.0.0.5:7890: connection refused',
          currentVersion: 'v0.21.0',
          channel: 'stable',
          hasUpdate: false,
          isDevBuild: false,
          latestVersion: '',
          releaseNotes: '',
          releaseUrl: '',
          publishedAt: '',
          checkedAt: '2026-07-20T00:00:00Z',
          cacheExpiresAt: '2026-07-20T06:00:00Z',
        })
      }),
    )
    const user = userEvent.setup()
    renderPage(<SystemVersionPage />)

    expect(await screen.findByText('检查更新失败')).toBeInTheDocument()
    expect(screen.getByText('查 release 列表失败: proxyconnect tcp 10.0.0.5:7890: connection refused')).toBeInTheDocument()
    expect(screen.queryByText('已是最新版本')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '应用更新' })).toBeDisabled()

    const checkButton = screen.getByRole('button', { name: '检查更新' })
    expect(checkButton).toBeEnabled()
    await user.click(checkButton)
    await waitFor(() => {
      expect(forceValues).toContain('true')
    })
  })

  it('触发更新后进度进入下载中（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<SystemVersionPage />)

    await screen.findByText('v0.21.0')
    await user.click(screen.getByRole('button', { name: '应用更新' }))

    // 确认弹窗
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: '开始更新' }))

    // 更新进度块进入下载中
    await waitFor(() => {
      expect(screen.getByText('下载中')).toBeInTheDocument()
    })
  })

  it('关闭自动检查时不发初始请求，手动检查仍使用 force=true', async () => {
    useScenario('normal')
    let settingsLoaded = false
    const forceValues: string[] = []
    server.use(
      http.get('/admin/v1/settings', () => {
        settingsLoaded = true
        return HttpResponse.json({
          items: [
            { key: 'update.auto-check-enabled', value: 'false' },
            { key: 'update.check-interval-hours', value: '6' },
          ],
        })
      }),
      http.get('/admin/v1/system/update-check', ({ request }) => {
        forceValues.push(new URL(request.url).searchParams.get('force') ?? '')
        return HttpResponse.json({
          status: 'ok',
          currentVersion: 'v0.21.0',
          channel: 'stable',
          hasUpdate: false,
          isDevBuild: false,
          latestVersion: 'v0.21.0',
          releaseNotes: '',
          releaseUrl: '',
          publishedAt: '',
          checkedAt: '2026-07-19T00:00:00Z',
          cacheExpiresAt: '2026-07-19T06:00:00Z',
        })
      }),
    )
    const user = userEvent.setup()
    renderPage(<SystemVersionPage />)

    await waitFor(() => {
      expect(settingsLoaded).toBe(true)
    })
    expect(forceValues).toHaveLength(0)

    await user.click(screen.getByRole('button', { name: '检查更新' }))
    await waitFor(() => {
      expect(forceValues).toEqual(['true'])
    })
    expect(await screen.findByText('v0.21.0')).toBeInTheDocument()
  })
})

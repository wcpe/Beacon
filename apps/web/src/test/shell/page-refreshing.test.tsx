import { QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'

import PageAsyncSection from '../../features/shared/page-async-section'
import '../../i18n'
import { resetPageRefreshingForTest } from '../../shell/page-refreshing'
import PageRefreshButton from '../../shell/page-refresh-button'

interface RefreshablePageProps {
  queryFn: () => Promise<string>
  refetchInterval?: number
}

function RefreshablePage({ queryFn, refetchInterval }: RefreshablePageProps) {
  const query = useQuery({
    queryKey: ['servers'],
    queryFn,
    refetchInterval,
    refetchOnWindowFocus: false,
    staleTime: Infinity,
  })

  return (
    <>
      <PageRefreshButton onRefreshingChange={() => undefined} />
      <main>
        <PageAsyncSection
          isLoading={query.isLoading}
          isError={query.isError}
          error={query.error}
          skeleton={<div data-testid="page-skeleton">加载中</div>}
        >
          <p>{query.data}</p>
        </PageAsyncSection>
      </main>
    </>
  )
}

function renderRefreshablePage(queryFn: () => Promise<string>, refetchInterval?: number) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        gcTime: Infinity,
        retry: false,
        staleTime: Infinity,
      },
    },
  })
  queryClient.setQueryData(['servers'], '缓存内容')

  const view = render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={['/servers']}>
        <RefreshablePage queryFn={queryFn} refetchInterval={refetchInterval} />
      </MemoryRouter>
    </QueryClientProvider>,
  )

  return { ...view, queryClient }
}

afterEach(() => {
  cleanup()
  resetPageRefreshingForTest()
  vi.clearAllTimers()
  vi.useRealTimers()
})

describe('页眉刷新内容区加载态', () => {
  it('点击页眉刷新后展示 aria-busy，刷新完成后收起', async () => {
    let finishRefresh: ((value: string) => void) | undefined
    const queryFn = vi.fn(
      () =>
        new Promise<string>((resolve) => {
          finishRefresh = resolve
        }),
    )
    const { container } = renderRefreshablePage(queryFn)
    const main = screen.getByRole('main')
    const refreshButton = container.querySelector('[data-slot="page-refresh"]')
    expect(refreshButton).not.toBeNull()

    fireEvent.click(refreshButton as HTMLElement)

    await waitFor(() => {
      expect(main.querySelector('[aria-busy="true"]')).not.toBeNull()
      expect(screen.getByTestId('page-skeleton')).toBeInTheDocument()
    })
    expect(queryFn).toHaveBeenCalledTimes(1)

    act(() => {
      finishRefresh?.('刷新完成')
    })

    await waitFor(() => {
      expect(screen.getByText('刷新完成')).toBeInTheDocument()
      expect(main.querySelector('[aria-busy="true"]')).toBeNull()
    })
  })

  it('后台八秒轮询不会触发页面刷新加载态', async () => {
    vi.useFakeTimers()
    const queryFn = vi.fn(() => Promise.resolve('轮询完成'))
    const { container } = renderRefreshablePage(queryFn, 8_000)
    const main = screen.getByRole('main')

    await act(async () => {
      await vi.advanceTimersByTimeAsync(8_000)
    })

    expect(queryFn).toHaveBeenCalledTimes(1)
    expect(main.querySelector('[aria-busy="true"]')).toBeNull()
    expect(container.querySelector('[data-testid="page-skeleton"]')).not.toBeInTheDocument()
  })
})

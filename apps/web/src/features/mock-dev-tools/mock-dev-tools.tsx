import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  useSyncExternalStore,
  type PointerEvent as ReactPointerEvent,
} from 'react'
import { useTranslation } from 'react-i18next'
import { SlidersHorizontal, X } from 'lucide-react'

import {
  getMockDataVolume,
  getMockLatency,
  getMockScenario,
  MOCK_DATA_VOLUMES,
  MOCK_LATENCIES,
  MOCK_SCENARIOS,
  setMockDataVolume,
  setMockLatency,
  setMockScenario,
  subscribeMockDataVolume,
  subscribeMockLatency,
  subscribeMockScenario,
  type MockScenario,
} from '@beacon/devmock/scenario'
import {
  Button,
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  cn,
} from '@beacon/ui'
import LifecycleMockReview from '../lifecycle/mock-review'

interface Position {
  left: number
  top: number
}

interface PointerDrag extends Position {
  pointerId: number
  startX: number
  startY: number
  moved: boolean
}

interface PanelPosition extends Position {
  ready: boolean
}

const POSITION_STORAGE_KEY = 'beacon-mock-dev-tools-position'
const BUTTON_SIZE = 48
const VIEWPORT_MARGIN = 12
const PANEL_GAP = 12

function clampPosition(position: Position): Position {
  const maxLeft = Math.max(VIEWPORT_MARGIN, window.innerWidth - BUTTON_SIZE - VIEWPORT_MARGIN)
  const maxTop = Math.max(VIEWPORT_MARGIN, window.innerHeight - BUTTON_SIZE - VIEWPORT_MARGIN)
  return {
    left: Math.min(maxLeft, Math.max(VIEWPORT_MARGIN, position.left)),
    top: Math.min(maxTop, Math.max(VIEWPORT_MARGIN, position.top)),
  }
}

function defaultPosition(): Position {
  if (typeof window === 'undefined') {
    return { left: VIEWPORT_MARGIN, top: VIEWPORT_MARGIN }
  }
  return clampPosition({
    left: window.innerWidth - BUTTON_SIZE - 24,
    top: window.innerHeight - BUTTON_SIZE - 24,
  })
}

function readPosition(): Position {
  if (typeof window === 'undefined') {
    return { left: VIEWPORT_MARGIN, top: VIEWPORT_MARGIN }
  }
  try {
    const stored = window.localStorage.getItem(POSITION_STORAGE_KEY)
    if (stored !== null) {
      const value = JSON.parse(stored) as Partial<Position>
      if (
        typeof value.left === 'number' &&
        Number.isFinite(value.left) &&
        typeof value.top === 'number' &&
        Number.isFinite(value.top)
      ) {
        return clampPosition({ left: value.left, top: value.top })
      }
    }
  } catch {
    // 持久化不可用或内容损坏时使用视口默认位置
  }
  return defaultPosition()
}

function persistPosition(position: Position): void {
  try {
    window.localStorage.setItem(POSITION_STORAGE_KEY, JSON.stringify(position))
  } catch {
    // 持久化失败不影响当前会话拖动
  }
}

export default function MockDevTools() {
  const { t } = useTranslation()
  const scenario = useSyncExternalStore(subscribeMockScenario, getMockScenario)
  const dataVolume = useSyncExternalStore(subscribeMockDataVolume, getMockDataVolume)
  const latency = useSyncExternalStore(subscribeMockLatency, getMockLatency)
  const [position, setPosition] = useState<Position>(readPosition)
  const [panelPosition, setPanelPosition] = useState<PanelPosition>({
    left: VIEWPORT_MARGIN,
    top: VIEWPORT_MARGIN,
    ready: false,
  })
  const [panelOpen, setPanelOpen] = useState(false)
  const [lifecycleReviewSubject, setLifecycleReviewSubject] = useState<'namespace' | 'server' | null>(null)
  const [viewportRevision, setViewportRevision] = useState(0)
  const dragRef = useRef<PointerDrag | null>(null)
  const suppressClickRef = useRef(false)
  const panelRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const onResize = () => {
      setPosition((current) => {
        const next = clampPosition(current)
        if (next.left === current.left && next.top === current.top) {
          return current
        }
        persistPosition(next)
        return next
      })
      setViewportRevision((current) => current + 1)
    }
    window.addEventListener('resize', onResize)
    return () => {
      window.removeEventListener('resize', onResize)
    }
  }, [])

  useEffect(() => {
    if (!panelOpen) {
      return
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setPanelOpen(false)
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [panelOpen])

  useLayoutEffect(() => {
    if (!panelOpen || panelRef.current === null) {
      return
    }
    const panel = panelRef.current.getBoundingClientRect()
    const opensLeft = position.left + BUTTON_SIZE + PANEL_GAP + panel.width > window.innerWidth - VIEWPORT_MARGIN
    const opensAbove = position.top + BUTTON_SIZE + PANEL_GAP + panel.height > window.innerHeight - VIEWPORT_MARGIN
    const proposedLeft = opensLeft
      ? position.left - panel.width - PANEL_GAP
      : position.left + BUTTON_SIZE + PANEL_GAP
    const proposedTop = opensAbove
      ? position.top - panel.height - PANEL_GAP
      : position.top + BUTTON_SIZE + PANEL_GAP
    const maxLeft = Math.max(VIEWPORT_MARGIN, window.innerWidth - panel.width - VIEWPORT_MARGIN)
    const maxTop = Math.max(VIEWPORT_MARGIN, window.innerHeight - panel.height - VIEWPORT_MARGIN)
    setPanelPosition({
      left: Math.min(maxLeft, Math.max(VIEWPORT_MARGIN, proposedLeft)),
      top: Math.min(maxTop, Math.max(VIEWPORT_MARGIN, proposedTop)),
      ready: true,
    })
  }, [panelOpen, position, viewportRevision])

  const finishDrag = (event: ReactPointerEvent<HTMLButtonElement>, suppressClick: boolean) => {
    const drag = dragRef.current
    if (drag === null) {
      return
    }
    if (drag.pointerId !== event.pointerId) {
      return
    }
    if (drag.moved) {
      const next = clampPosition({
        left: drag.left + event.clientX - drag.startX,
        top: drag.top + event.clientY - drag.startY,
      })
      setPosition(next)
      persistPosition(next)
      if (suppressClick) {
        suppressClickRef.current = true
        window.setTimeout(() => {
          suppressClickRef.current = false
        }, 0)
      }
    }
    dragRef.current = null
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId)
    }
  }

  const onPointerDown = (event: ReactPointerEvent<HTMLButtonElement>) => {
    if (!event.isPrimary || event.button !== 0) {
      return
    }
    dragRef.current = {
      pointerId: event.pointerId,
      startX: event.clientX,
      startY: event.clientY,
      left: position.left,
      top: position.top,
      moved: false,
    }
    event.currentTarget.setPointerCapture(event.pointerId)
  }

  const onPointerMove = (event: ReactPointerEvent<HTMLButtonElement>) => {
    const drag = dragRef.current
    if (drag === null) {
      return
    }
    if (drag.pointerId !== event.pointerId) {
      return
    }
    const deltaX = event.clientX - drag.startX
    const deltaY = event.clientY - drag.startY
    if (!drag.moved && Math.hypot(deltaX, deltaY) < 4) {
      return
    }
    drag.moved = true
    setPosition(clampPosition({ left: drag.left + deltaX, top: drag.top + deltaY }))
  }

  const onButtonClick = () => {
    if (suppressClickRef.current) {
      suppressClickRef.current = false
      return
    }
    setPanelOpen((current) => !current)
  }

  const volumeLabel = t(`common.mockDevTools.volume.${dataVolume}`)
  const latencyLabel = t(`common.mockDevTools.latencyTier.${latency}`)

  return (
    <>
      <div
        data-slot="mock-dev-tools-trigger-position"
        className="fixed z-50"
        style={{ left: position.left, top: position.top }}
      >
        <Button
          type="button"
          size="icon-lg"
          variant="default"
          aria-label={panelOpen ? t('common.mockDevTools.close') : t('common.mockDevTools.open')}
          aria-expanded={panelOpen}
          aria-controls="mock-dev-tools-panel"
          title={t('common.mockDevTools.dragHint')}
          className="size-12 touch-none rounded-full border border-primary-foreground/20 shadow-pop transition-transform hover:scale-105 active:scale-100"
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={(event) => {
            finishDrag(event, true)
          }}
          onPointerCancel={(event) => {
            finishDrag(event, false)
          }}
          onClick={onButtonClick}
        >
          <SlidersHorizontal className="size-5" />
        </Button>
      </div>

      {panelOpen ? (
        <div
          ref={panelRef}
          id="mock-dev-tools-panel"
          data-slot="mock-dev-tools-panel"
          role="region"
          aria-label={t('common.mockDevTools.title')}
          className="fixed z-50"
          style={{
            left: panelPosition.left,
            top: panelPosition.top,
            width: 352,
            maxWidth: 'calc(100vw - 24px)',
            maxHeight: 'calc(100vh - 24px)',
            visibility: panelPosition.ready ? 'visible' : 'hidden',
          }}
        >
          <Card className="max-h-[calc(100vh-24px)] overflow-y-auto rounded-xl border-border/70 shadow-pop">
            <CardHeader className="grid-cols-[1fr_auto] items-start gap-3 pb-3">
              <div className="grid gap-1">
                <CardTitle>{t('common.mockDevTools.title')}</CardTitle>
                <CardDescription>{t('common.mockDevTools.dragHint')}</CardDescription>
              </div>
              <Button
                type="button"
                size="icon-sm"
                variant="ghost"
                aria-label={t('common.mockDevTools.close')}
                onClick={() => {
                  setPanelOpen(false)
                }}
              >
                <X className="size-4" />
              </Button>
            </CardHeader>
            <CardContent className="grid gap-4">
              <section className="grid gap-2" aria-labelledby="mock-dev-tools-volume-label">
                <h3 id="mock-dev-tools-volume-label" className="text-xs font-semibold text-ink-2">
                  {t('common.mockDevTools.dataVolume')}
                </h3>
                <div role="group" aria-label={t('common.mockDevTools.dataVolume')} className="grid grid-cols-3 gap-1.5">
                  {MOCK_DATA_VOLUMES.map((option) => (
                    <Button
                      key={option.value}
                      type="button"
                      size="sm"
                      variant={dataVolume === option.value ? 'secondary' : 'outline'}
                      aria-pressed={dataVolume === option.value}
                      className={cn(
                        'h-auto min-h-9 whitespace-normal px-1.5 py-1.5 text-[11px] leading-tight',
                        dataVolume === option.value && 'border-brand/40 bg-brand-50 text-brand',
                      )}
                      onClick={() => {
                        setMockDataVolume(option.value)
                      }}
                    >
                      {t(`common.mockDevTools.volume.${option.value}`)}
                    </Button>
                  ))}
                </div>
              </section>

              <section className="grid gap-2" aria-labelledby="mock-dev-tools-latency-label">
                <h3 id="mock-dev-tools-latency-label" className="text-xs font-semibold text-ink-2">
                  {t('common.mockDevTools.latency')}
                </h3>
                <div role="group" aria-label={t('common.mockDevTools.latency')} className="grid grid-cols-2 gap-1.5">
                  {MOCK_LATENCIES.map((option) => (
                    <Button
                      key={option.value}
                      type="button"
                      size="sm"
                      variant={latency === option.value ? 'secondary' : 'outline'}
                      aria-pressed={latency === option.value}
                      className={cn(
                        'h-auto min-h-9 whitespace-normal px-2 py-1.5 text-xs leading-tight',
                        latency === option.value && 'border-brand/40 bg-brand-50 text-brand',
                      )}
                      onClick={() => {
                        setMockLatency(option.value)
                      }}
                    >
                      {t(`common.mockDevTools.latencyTier.${option.value}`)}
                    </Button>
                  ))}
                </div>
              </section>

              <div
                data-slot="mock-dev-tools-combination"
                className="grid gap-1 rounded-lg border border-border/70 bg-muted/50 px-3 py-2"
              >
                <span className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                  {t('common.mockDevTools.currentCombination')}
                </span>
                <strong className="text-sm font-semibold text-ink-1">
                  {t('common.mockDevTools.combination', {
                    volume: volumeLabel,
                    latency: latencyLabel,
                  })}
                </strong>
                <p className="text-xs leading-relaxed text-muted-foreground">
                  {t('common.mockDevTools.cartesianHint')}
                </p>
              </div>

              <section className="grid gap-2" aria-labelledby="mock-dev-tools-scene-label">
                <h3 id="mock-dev-tools-scene-label" className="text-xs font-semibold text-ink-2">
                  {t('common.mockDevTools.scene')}
                </h3>
                <div role="group" aria-label={t('common.mockDevTools.scene')} className="grid grid-cols-4 gap-1.5">
                  {MOCK_SCENARIOS.map((option: MockScenario) => (
                    <Button
                      key={option}
                      type="button"
                      size="xs"
                      variant={scenario === option ? 'secondary' : 'outline'}
                      aria-pressed={scenario === option}
                      className={cn(
                        'px-1 text-[11px]',
                        scenario === option && 'border-brand/40 bg-brand-50 text-brand',
                      )}
                      onClick={() => {
                        setMockScenario(option)
                      }}
                    >
                      {t(`common.scenario.${option}`)}
                    </Button>
                  ))}
                </div>
                <p className="text-xs leading-relaxed text-muted-foreground">
                  {t('common.mockDevTools.priorityHint')}
                </p>
              </section>

              <section className="grid gap-2" aria-labelledby="mock-dev-tools-lifecycle-review-label">
                <h3 id="mock-dev-tools-lifecycle-review-label" className="text-xs font-semibold text-ink-2">
                  {t('common.mockDevTools.lifecycleReview.title')}
                </h3>
                <div className="grid grid-cols-2 gap-1.5">
                  <Button
                    type="button"
                    size="xs"
                    variant="outline"
                    onClick={() => {
                      setLifecycleReviewSubject('server')
                    }}
                  >
                    {t('common.mockDevTools.lifecycleReview.server')}
                  </Button>
                  <Button
                    type="button"
                    size="xs"
                    variant="outline"
                    onClick={() => {
                      setLifecycleReviewSubject('namespace')
                    }}
                  >
                    {t('common.mockDevTools.lifecycleReview.namespace')}
                  </Button>
                </div>
              </section>

              <Button
                type="button"
                variant="outline"
                size="sm"
                className="w-full"
                onClick={() => {
                  setMockScenario('normal')
                  setMockDataVolume('baseline')
                  setMockLatency('none')
                }}
              >
                {t('common.mockDevTools.reset')}
              </Button>
            </CardContent>
          </Card>
        </div>
      ) : null}

      <Dialog
        open={lifecycleReviewSubject !== null}
        onOpenChange={(open) => {
          if (!open) {
            setLifecycleReviewSubject(null)
          }
        }}
      >
        <DialogContent className="grid max-h-[calc(100dvh-2rem)] grid-rows-[auto_minmax(0,1fr)] overflow-hidden sm:max-w-4xl">
          <DialogHeader>
            <DialogTitle>{t('common.mockDevTools.lifecycleReview.title')}</DialogTitle>
          </DialogHeader>
          {lifecycleReviewSubject !== null ? (
            <div className="min-h-0 overflow-y-auto pr-1">
              <LifecycleMockReview subject={lifecycleReviewSubject} />
            </div>
          ) : null}
        </DialogContent>
      </Dialog>
    </>
  )
}

// 编辑器内容区：视图切换工具栏（编辑 / Diff / 生效预览）+ 对应内容 + 底部历史修订面板。

import { useTranslation } from 'react-i18next'
import CodeEditor from '../../components/CodeEditor'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import type { DiffView, InstanceView, RevisionView } from '../../api/types'
import type { EffectiveConfigView } from '../../api/client'
import type { OpenTab, ViewMode } from './types'
import EffectivePreview from './EffectivePreview'
import RevisionHistory from './RevisionHistory'

interface ConfigEditorPaneProps {
  tab: OpenTab
  view: ViewMode
  // 切换到编辑 / Diff 视图
  onSetView: (mode: ViewMode) => void
  // 切换到生效预览（同时设定默认预览目标）
  onActivateEffective: () => void
  // 编辑态内容变更
  editor: { onChange: (content: string) => void }
  // 保存
  save: { onSave: () => void; saving: boolean }
  // 复制到实例：以当前配置为底新建一条 server 层覆盖（预填源内容，进入编辑改 diff）
  onCopyToInstance: () => void
  // Diff 视图：可选版本、当前选择、切换、对比数据
  diff: {
    versionNumbers: number[]
    selected: { from: string; to: string }
    onChange: (next: { from: string; to: string }) => void
    data: DiffView | null | undefined
  }
  // 生效预览
  effective: {
    instances: InstanceView[]
    target: { serverId?: string; group?: string }
    onTargetChange: (t: { serverId?: string; group?: string }) => void
    isLoading: boolean
    data: EffectiveConfigView | null | undefined
  }
  // 历史修订面板
  history: {
    revisions: RevisionView[]
    collapsed: boolean
    highlightRev?: number
    onToggleCollapse: () => void
    onSelectRevision: (rev: RevisionView) => void
  }
}

export default function ConfigEditorPane({
  tab,
  view,
  onSetView,
  onActivateEffective,
  editor,
  save,
  onCopyToInstance,
  diff,
  effective,
  history,
}: ConfigEditorPaneProps) {
  const { t } = useTranslation()
  return (
    <div className="flex-1 flex flex-col min-h-0 rounded-lg border border-border overflow-hidden bg-background">
      {/* 视图切换 + 保存按钮 */}
      <div className="flex-shrink-0 flex items-center justify-between px-3 py-1 border-b border-border bg-muted/30">
        <div className="flex items-center gap-1">
          <Button
            variant={view === 'edit' ? 'default' : 'ghost'}
            size="xs"
            className={cn('h-7 px-3 text-xs')}
            onClick={() => onSetView('edit')}
          >
            {t('configs.viewEdit')}
          </Button>
          <Button
            variant={view === 'diff' ? 'default' : 'ghost'}
            size="xs"
            className={cn('h-7 px-3 text-xs')}
            onClick={() => onSetView('diff')}
          >
            {t('configs.viewDiff')}
          </Button>
          <Button
            variant={view === 'effective' ? 'default' : 'ghost'}
            size="xs"
            className={cn('h-7 px-3 text-xs')}
            onClick={onActivateEffective}
          >
            {t('configs.viewEffective')}
          </Button>
        </div>

        <div className="flex items-center gap-2">
          {view === 'diff' && diff.versionNumbers.length >= 2 && (
            <div className="flex items-center gap-1">
              <select
                className="h-7 rounded border border-input bg-background px-1.5 text-xs text-foreground"
                value={diff.selected.from}
                onChange={(e) => diff.onChange({ ...diff.selected, from: e.target.value })}
              >
                <option value="">{t('configs.diffFromPlaceholder')}</option>
                {diff.versionNumbers.map((v) => (
                  <option key={v} value={v}>
                    v{v}
                  </option>
                ))}
              </select>
              <span className="text-xs text-gray-500">→</span>
              <select
                className="h-7 rounded border border-input bg-background px-1.5 text-xs text-foreground"
                value={diff.selected.to}
                onChange={(e) => diff.onChange({ ...diff.selected, to: e.target.value })}
              >
                <option value="">{t('configs.diffToPlaceholder')}</option>
                {diff.versionNumbers.map((v) => (
                  <option key={v} value={v}>
                    v{v}
                  </option>
                ))}
              </select>
            </div>
          )}
          <Button
            variant="outline"
            size="xs"
            className="h-7 px-3 text-xs"
            onClick={onCopyToInstance}
          >
            {t('configs.copyToInstance')}
          </Button>
          <Button
            size="xs"
            className="h-7 px-3 text-xs bg-primary hover:bg-primary/80 text-primary-foreground"
            onClick={save.onSave}
            disabled={save.saving}
          >
            {save.saving ? t('configs.saving') : t('configs.saveBtn')}
          </Button>
        </div>
      </div>

      {/* 内容区：编辑 / 生效预览 / Diff */}
      <div className="flex-1 min-h-0">
        {view === 'edit' ? (
          <CodeEditor value={tab.content} language={tab.format} onChange={editor.onChange} />
        ) : view === 'effective' ? (
          <EffectivePreview
            instances={effective.instances}
            target={effective.target}
            onTargetChange={effective.onTargetChange}
            isLoading={effective.isLoading}
            data={effective.data}
          />
        ) : (
          <CodeEditor
            original={diff.data?.fromContent ?? ''}
            modified={diff.data?.toContent ?? tab.content}
            language={tab.format}
          />
        )}
      </div>

      {/* 底部历史修订面板 */}
      <RevisionHistory
        revisions={history.revisions}
        collapsed={history.collapsed}
        highlightRev={history.highlightRev}
        onToggleCollapse={history.onToggleCollapse}
        onSelectRevision={history.onSelectRevision}
      />
    </div>
  )
}

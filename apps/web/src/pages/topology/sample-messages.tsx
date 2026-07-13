// 链路样本消息列表（拓扑两个侧面板共用）：样本 messageId + 「查看 payload」入口。
// payload 属敏感内容：仅经受控弹窗按需查看（原因必填 + 后端先审计后返回），本列表只展示 id。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Eye } from 'lucide-react'

import PayloadDialog from '../../features/observability/payload-dialog'

interface SampleMessagesProps {
  ids: string[]
}

export default function SampleMessages({ ids }: SampleMessagesProps) {
  const { t } = useTranslation()
  // 正在查看 payload 的消息 id；null 表示弹窗关闭（按 id 重挂载弹窗，草稿不跨消息残留）
  const [viewingId, setViewingId] = useState<string | null>(null)

  return (
    <div>
      <p className="text-[11px] font-semibold tracking-[0.3px] text-ink-4 uppercase">
        {t('cluster.topology.edges.sampleMessages')}
      </p>
      <ul className="mt-1 grid gap-0.5">
        {ids.map((id) => (
          <li key={id} className="flex items-center gap-1.5">
            <span className="min-w-0 flex-1 truncate font-mono text-xs text-ink-3">{id}</span>
            <button
              type="button"
              className="flex shrink-0 items-center gap-0.5 text-[11px] text-brand-600 hover:underline"
              onClick={() => {
                setViewingId(id)
              }}
            >
              <Eye className="size-3" />
              {t('cluster.topology.payload.view')}
            </button>
          </li>
        ))}
      </ul>
      {viewingId !== null && (
        <PayloadDialog
          key={viewingId}
          messageId={viewingId}
          onClose={() => {
            setViewingId(null)
          }}
        />
      )}
    </div>
  )
}

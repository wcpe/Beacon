// MCP 工具调用流水页（/mcp-invocations，系统大域）：外部 Agent 工具调用的取证与安全观测。
//
// 布局沿用已拍板的主从范式（UX.md §5）：主列 = 吸顶筛选条 + 自区滚列表 + 吸底游标分页，
// 右侧 = 非模态详情抽屉（行点击打开，Esc / 点外部收起）。列表与详情各管各的取数状态。
//
// 范围说明：本页只读（契约 §3.7 无写入端点），只呈现**脱敏摘要**的调用流水，不展示参数正文。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import MasterDetail from '../features/shared/master-detail'
import InvocationDetailPanel from './mcp-invocations/invocation-detail-panel'
import InvocationList from './mcp-invocations/invocation-list'

export default function McpInvocationsPage() {
  const { t } = useTranslation()
  // 当前查看详情的调用 ID（null 表示右侧详情列收起）
  const [selectedId, setSelectedId] = useState<string | null>(null)

  return (
    <section className="grid gap-4">
      <MasterDetail
        master={
          <InvocationList
            selectedId={selectedId}
            onView={(row) => {
              setSelectedId(row.invocationId)
            }}
          />
        }
        detail={selectedId === null ? null : <InvocationDetailPanel invocationId={selectedId} />}
        detailTitle={t('system.mcpInvocations.detailTitle')}
        closeLabel={t('system.common.close')}
        onClose={() => {
          setSelectedId(null)
        }}
      />
    </section>
  )
}

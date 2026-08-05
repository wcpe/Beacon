import { useQuery } from '@tanstack/react-query'
import { fetchApprovals } from '../api/approvals'

export default function ApprovalNavBadge() {
  const query = useQuery({
    queryKey: ['approvals', 'pending-count'],
    queryFn: () => fetchApprovals({ status: 'pending', page: 1, pageSize: 1 }),
    staleTime: 15_000,
    refetchInterval: 30_000,
    retry: false,
  })
  const count = query.data?.total ?? 0
  if (query.isError || count === 0) return null
  return <span data-slot="approval-pending-badge" className="ml-auto rounded-full bg-destructive px-1.5 py-0.5 text-[10px] font-semibold tabular-nums text-white">{count > 999 ? '999+' : count}</span>
}

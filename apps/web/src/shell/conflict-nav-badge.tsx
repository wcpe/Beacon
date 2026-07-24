// 侧栏「身份冲突」项的红点计数（FR-177）：有并发身份冲突时显示红色计数徽标，无冲突时不渲染。
// 与冲突页共用 query key，处置后计数自动随缓存失效刷新。
import { useQuery } from '@tanstack/react-query'

import { fetchIdentities } from '../api/cluster'

export default function ConflictNavBadge() {
  const query = useQuery({
    queryKey: ['identities', 'conflict', undefined],
    queryFn: () => fetchIdentities({ status: 'conflict', pageSize: 100 }),
  })
  const count = query.data?.items.length ?? 0
  if (count === 0) {
    return null
  }
  return (
    <span className="ml-auto grid min-w-[18px] place-items-center rounded-full bg-crit px-1.5 text-[10px] font-bold tabular-nums text-white">
      {count}
    </span>
  )
}

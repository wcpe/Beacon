// 交付大域行级双栏文本 diff（不依赖 Monaco，自绘）：基于 LCS 求最小编辑，
// 左栏标删除、右栏标新增，未变行两栏并排。用于文件资产两侧 diff 与配置内容对比。
import { useMemo } from 'react'

import { cn } from '@beacon/ui'

interface TextDiffProps {
  left: string
  right: string
  leftLabel: string
  rightLabel: string
}

// 单行 diff 结果：same 两栏同行；del 仅左；add 仅右
type DiffRow =
  | { kind: 'same'; left: string; right: string }
  | { kind: 'del'; left: string }
  | { kind: 'add'; right: string }

// 经典 LCS 动态规划求两序列最长公共子序列，回溯得到最小编辑脚本
function diffLines(leftLines: string[], rightLines: string[]): DiffRow[] {
  const m = leftLines.length
  const n = rightLines.length
  // dp[i][j]：leftLines[i:] 与 rightLines[j:] 的 LCS 长度
  const dp: number[][] = Array.from({ length: m + 1 }, () => new Array<number>(n + 1).fill(0))
  for (let i = m - 1; i >= 0; i--) {
    for (let j = n - 1; j >= 0; j--) {
      dp[i][j] =
        leftLines[i] === rightLines[j]
          ? dp[i + 1][j + 1] + 1
          : Math.max(dp[i + 1][j], dp[i][j + 1])
    }
  }
  const rows: DiffRow[] = []
  let i = 0
  let j = 0
  while (i < m && j < n) {
    if (leftLines[i] === rightLines[j]) {
      rows.push({ kind: 'same', left: leftLines[i], right: rightLines[j] })
      i++
      j++
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      rows.push({ kind: 'del', left: leftLines[i] })
      i++
    } else {
      rows.push({ kind: 'add', right: rightLines[j] })
      j++
    }
  }
  while (i < m) {
    rows.push({ kind: 'del', left: leftLines[i] })
    i++
  }
  while (j < n) {
    rows.push({ kind: 'add', right: rightLines[j] })
    j++
  }
  return rows
}

export default function TextDiff({ left, right, leftLabel, rightLabel }: TextDiffProps) {
  const rows = useMemo(() => diffLines(left.split('\n'), right.split('\n')), [left, right])
  return (
    <div className="overflow-x-auto rounded-md border">
      <div className="grid grid-cols-2 border-b bg-muted/50 text-xs font-medium">
        <div className="px-3 py-1.5">{leftLabel}</div>
        <div className="border-l px-3 py-1.5">{rightLabel}</div>
      </div>
      <div className="grid grid-cols-2 font-mono text-xs leading-relaxed">
        <div>
          {rows.map((row, idx) =>
            row.kind === 'add' ? (
              <div key={idx} className="min-h-[1.4em] bg-muted/30 px-3">
                &nbsp;
              </div>
            ) : (
              <div
                key={idx}
                className={cn('whitespace-pre-wrap px-3', row.kind === 'del' && 'bg-destructive/15')}
              >
                {row.left === '' ? ' ' : row.left}
              </div>
            ),
          )}
        </div>
        <div className="border-l">
          {rows.map((row, idx) =>
            row.kind === 'del' ? (
              <div key={idx} className="min-h-[1.4em] bg-muted/30 px-3">
                &nbsp;
              </div>
            ) : (
              <div
                key={idx}
                className={cn(
                  'whitespace-pre-wrap px-3',
                  row.kind === 'add' && 'bg-green-500/15',
                )}
              >
                {row.right === '' ? ' ' : row.right}
              </div>
            ),
          )}
        </div>
      </div>
    </div>
  )
}

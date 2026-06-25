// DataTable 单测：表头、行渲染、空态、行点击。
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import DataTable, { type DataTableColumn } from './DataTable'

interface Row {
  id: number
  name: string
}

const columns: DataTableColumn<Row>[] = [
  { header: '编号', cell: (r) => r.id },
  { header: '名称', cell: (r) => r.name },
]

describe('DataTable', () => {
  it('渲染表头与行数据', () => {
    const rows: Row[] = [
      { id: 1, name: '甲' },
      { id: 2, name: '乙' },
    ]
    render(<DataTable columns={columns} rows={rows} rowKey={(r) => String(r.id)} />)
    expect(screen.getByText('编号')).toBeInTheDocument()
    expect(screen.getByText('名称')).toBeInTheDocument()
    expect(screen.getByText('甲')).toBeInTheDocument()
    expect(screen.getByText('乙')).toBeInTheDocument()
  })

  it('空数据显示空态文案且跨所有列', () => {
    render(
      <DataTable columns={columns} rows={[]} rowKey={(r) => String(r.id)} emptyText="暂无环境" />,
    )
    const cell = screen.getByText('暂无环境')
    expect(cell).toBeInTheDocument()
    expect(cell).toHaveAttribute('colspan', String(columns.length))
  })

  it('rows 为 undefined 时按空态处理', () => {
    render(<DataTable columns={columns} rows={undefined} rowKey={(r) => String(r.id)} />)
    expect(screen.getByText('暂无数据')).toBeInTheDocument()
  })

  it('点击行触发回调', async () => {
    const onRowClick = vi.fn()
    const rows: Row[] = [{ id: 1, name: '甲' }]
    render(
      <DataTable
        columns={columns}
        rows={rows}
        rowKey={(r) => String(r.id)}
        onRowClick={onRowClick}
      />,
    )
    await userEvent.click(screen.getByText('甲'))
    expect(onRowClick).toHaveBeenCalledWith(rows[0])
  })
})

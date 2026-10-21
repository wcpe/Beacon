import { createRef } from 'react'

import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'

import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogOverlay,
  AlertDialogTitle,
  AlertDialogTrigger,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogOverlay,
  DialogTitle,
  DialogTrigger,
  Sheet,
  SheetContent,
  SheetDescription,
  SheetTitle,
  SheetTrigger,
} from '@beacon/ui'

// 三个 Overlay 组件都是 React.forwardRef，其 ref 泛型本次由 React.ElementRef 改为
// React.ComponentRef（React 19 已把 ElementRef 标为 deprecated，指向 ComponentRef）。
// 类型改动本身由 tsc 覆盖；本文件补的是运行期面：升级 react 到 19 同时改变了 ref 语义
// （ref 可作普通 prop 传递、支持 ref 清理函数等），需确认 forwardRef 组合下 ref 仍拿到真实 DOM。
//
// 这三个组件此前零测试覆盖，而业务侧共 14 处使用（Dialog 5 / Sheet 4 / AlertDialog 5）。

afterEach(cleanup)

describe('Overlay 的 ref 转发在 React 19 下仍指向真实 DOM', () => {
  it('DialogOverlay 的 ref 拿到 data-slot=dialog-overlay 的 div', () => {
    const ref = createRef<HTMLDivElement>()

    // Overlay 必须挂在 Dialog 内（Radix 约束）：用 defaultOpen 让它立即渲染，
    // 无需模拟点击即可验证 ref 转发。
    render(
      <Dialog open>
        <DialogOverlay ref={ref} />
      </Dialog>,
    )

    expect(ref.current).toBeInstanceOf(HTMLDivElement)
    expect(ref.current?.getAttribute('data-slot')).toBe('dialog-overlay')
  })

  it('AlertDialogOverlay 的 ref 拿到 data-slot=alert-dialog-overlay 的 div', () => {
    const ref = createRef<HTMLDivElement>()

    render(
      <AlertDialog open>
        <AlertDialogOverlay ref={ref} />
      </AlertDialog>,
    )

    expect(ref.current).toBeInstanceOf(HTMLDivElement)
    expect(ref.current?.getAttribute('data-slot')).toBe('alert-dialog-overlay')
  })

  it('SheetContent 渲染出的 overlay 带 data-slot=sheet-overlay', async () => {
    const user = userEvent.setup()

    // SheetOverlay 未从 index 导出（仅在 SheetContent 内部使用），故经 SheetContent 打开后
    // 按 data-slot 断言其存在——同样覆盖「overlay 在 React 19 下正常挂载」这一面。
    render(
      <Sheet>
        <SheetTrigger>打开抽屉</SheetTrigger>
        <SheetContent>
          <SheetTitle>抽屉标题</SheetTitle>
          <SheetDescription>抽屉描述</SheetDescription>
        </SheetContent>
      </Sheet>,
    )

    await user.click(screen.getByRole('button', { name: '打开抽屉' }))
    await waitFor(() => { expect(screen.getByText('抽屉标题')).toBeTruthy(); })

    expect(document.querySelector('[data-slot="sheet-overlay"]')).toBeInstanceOf(HTMLElement)
  })
})

describe('三类弹层在 React 19 下的开闭交互', () => {
  it('Dialog 由触发器打开后内容可见', async () => {
    const user = userEvent.setup()

    render(
      <Dialog>
        <DialogTrigger>打开对话框</DialogTrigger>
        <DialogContent>
          <DialogTitle>对话框标题</DialogTitle>
          <DialogDescription>对话框描述</DialogDescription>
        </DialogContent>
      </Dialog>,
    )

    expect(screen.queryByText('对话框标题')).toBeNull()

    await user.click(screen.getByRole('button', { name: '打开对话框' }))

    await waitFor(() => { expect(screen.getByText('对话框标题')).toBeTruthy(); })
  })

  it('Sheet 由触发器打开后内容可见', async () => {
    const user = userEvent.setup()

    render(
      <Sheet>
        <SheetTrigger>打开抽屉</SheetTrigger>
        <SheetContent>
          <SheetTitle>抽屉标题</SheetTitle>
          <SheetDescription>抽屉描述</SheetDescription>
        </SheetContent>
      </Sheet>,
    )

    expect(screen.queryByText('抽屉标题')).toBeNull()

    await user.click(screen.getByRole('button', { name: '打开抽屉' }))

    await waitFor(() => { expect(screen.getByText('抽屉标题')).toBeTruthy(); })
  })

  it('AlertDialog 由触发器打开后内容可见', async () => {
    const user = userEvent.setup()

    render(
      <AlertDialog>
        <AlertDialogTrigger>打开确认框</AlertDialogTrigger>
        <AlertDialogContent>
          <AlertDialogTitle>确认标题</AlertDialogTitle>
          <AlertDialogDescription>确认描述</AlertDialogDescription>
        </AlertDialogContent>
      </AlertDialog>,
    )

    expect(screen.queryByText('确认标题')).toBeNull()

    await user.click(screen.getByRole('button', { name: '打开确认框' }))

    await waitFor(() => { expect(screen.getByText('确认标题')).toBeTruthy(); })
  })
})

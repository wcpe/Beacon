// 动态浏览器标签标题（FR-123）：把页面「位置」拼成「<页名> - Beacon」写入 document.title。
// 页名为空 / 未知时回退为「Beacon」。各页 / 布局传入当前位置名即可，卸载不还原（下一处会覆盖）。

import { useEffect } from 'react'

// 应用名后缀（标签标题统一以「 - Beacon」收尾）。
const APP_NAME = 'Beacon'

// useDocumentTitle 按当前页名设置标签标题：有名 → 「<页名> - Beacon」；无名 → 「Beacon」。
export function useDocumentTitle(pageName?: string | null): void {
  useEffect(() => {
    const name = pageName?.trim()
    document.title = name ? `${name} - ${APP_NAME}` : APP_NAME
  }, [pageName])
}

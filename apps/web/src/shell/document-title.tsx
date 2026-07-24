// 浏览器标签标题：随路由切换为「Beacon - 当前页面」。
// 登录页单独写死；管理台页从 ALL_PAGES 的 titleKey 取 i18n 文案。
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { useLocation } from 'react-router-dom'

import { ALL_PAGES } from '../routes'

const BRAND = 'Beacon'

/** 由 pathname 解析页面标题；未知路径回落为品牌名。 */
function titleForPath(pathname: string, t: (key: string) => string): string {
  if (pathname === '/login') {
    return '登录'
  }
  // FR-190：站内许可页不进主导航 ALL_PAGES
  if (pathname === '/license') {
    return t('common.license.pageTitle')
  }
  const page = ALL_PAGES.find((p) => p.path === pathname)
  if (!page) {
    return BRAND
  }
  return t(page.titleKey)
}

/** 订阅路由与语言，写 document.title = "Beacon - {页面}"；品牌页仅 "Beacon"。 */
export default function DocumentTitle(): null {
  const { t, i18n } = useTranslation()
  const { pathname } = useLocation()

  useEffect(() => {
    const pageTitle = titleForPath(pathname, t)
    document.title = pageTitle === BRAND ? BRAND : `${BRAND} - ${pageTitle}`
  }, [pathname, t, i18n.language])

  return null
}

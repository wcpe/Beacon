// i18n 初始化与资源聚合：按域拆分资源文件（nav/common + 五个页面域），
// 后续并行页面 agent 各自只改所属域文件，避免在同一文件上冲突。
import i18next from 'i18next'
import { initReactI18next } from 'react-i18next'

import { auth } from './auth'
import { cluster } from './cluster'
import { common } from './common'
import { dashboard } from './dashboard'
import { delivery } from './delivery'
import { nav } from './nav'
import { observability } from './observability'
import { system } from './system'

void i18next.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  interpolation: {
    escapeValue: false,
  },
  resources: {
    'zh-CN': {
      translation: {
        nav,
        common,
        dashboard,
        cluster,
        observability,
        delivery,
        system,
        auth,
      },
    },
  },
})

export { i18next }

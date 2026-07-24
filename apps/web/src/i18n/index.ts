// i18n 初始化与资源聚合：按域拆分资源文件（nav/common + 五个页面域），
// 后续并行页面 agent 各自只改所属域文件，避免在同一文件上冲突。
// FR-194：en 现为全站业务镜像（apps/web/src/i18n/en/*）；缺键仍 fallback 到 zh-CN，不白屏。
import i18next from 'i18next'
import { initReactI18next } from 'react-i18next'

import { auth } from './auth'
import { cluster } from './cluster'
import { common } from './common'
import { dashboard } from './dashboard'
import { delivery } from './delivery'
import { enCommon } from './en-common'
import { auth as enAuth } from './en/auth'
import { cluster as enCluster } from './en/cluster'
import { dashboard as enDashboard } from './en/dashboard'
import { delivery as enDelivery } from './en/delivery'
import { nav as enNav } from './en/nav'
import { observability as enObservability } from './en/observability'
import { system as enSystem } from './en/system'
import { nav } from './nav'
import { observability } from './observability'
import { system } from './system'
import { currentLocale } from '../state/locale'

void i18next.use(initReactI18next).init({
  lng: currentLocale(),
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
    en: {
      translation: {
        nav: enNav,
        common: enCommon,
        dashboard: enDashboard,
        cluster: enCluster,
        observability: enObservability,
        delivery: enDelivery,
        system: enSystem,
        auth: enAuth,
      },
    },
  },
})

export { i18next }

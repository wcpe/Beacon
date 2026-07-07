import i18next from 'i18next'
import { initReactI18next } from 'react-i18next'

void i18next.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  interpolation: {
    escapeValue: false,
  },
  resources: {
    'zh-CN': {
      translation: {
        nav: {
          overview: '总览',
          topology: '拓扑',
          settings: '设置',
        },
        status: {
          ready: '脚手架就绪',
          mock: '等待 mock 基建',
          legacy: 'Legacy 冻结中',
        },
      },
    },
  },
})

export { i18next }

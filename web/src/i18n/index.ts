// 管理台国际化（i18n）初始化（FR-50，见 ADR-0033）。
// 关键约束：同步初始化 + 资源内联（不走异步 HTTP 后端加载），
// 保证生产与测试（vitest jsdom）环境下 t() 都同步返回真实 zh-CN 文案、永不出现裸 key。
// 本期只交付 zh-CN 全量；加语言只需补一份 locales/<lng>.ts 并在 resources 注册，组件零改动。

import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import { zhCN } from './locales/zh-CN'

// 默认命名空间名（单一资源树，不拆多 namespace，键内已分层）
const DEFAULT_NS = 'translation'

// 同步初始化：resources 内联、lng 与 fallbackLng 均 zh-CN。
// 已初始化则跳过（main.tsx 与测试 setup 各 import 一次，避免重复 init 告警）。
if (!i18n.isInitialized) {
  void i18n.use(initReactI18next).init({
    resources: {
      'zh-CN': { [DEFAULT_NS]: zhCN },
    },
    lng: 'zh-CN',
    fallbackLng: 'zh-CN',
    defaultNS: DEFAULT_NS,
    // 文案不含 HTML，交由 React 转义，关闭 i18next 自带插值转义
    interpolation: { escapeValue: false },
    // 键不存在时不打印告警刷屏（缺键回退已由 fallbackLng / defaultValue 兜底）
    react: { useSuspense: false },
  })
}

export default i18n

// 页眉语言切换状态（FR-194）：zh-CN / en，localStorage 持久；缺键 fallback 到 zh-CN 不白屏。
import { useSyncExternalStore } from 'react'

const LOCALE_KEY = 'beacon.locale'
export type AppLocale = 'zh-CN' | 'en'

const listeners = new Set<() => void>()
let snapshot: AppLocale = readFromStorage()

function readFromStorage(): AppLocale {
  try {
    const raw = localStorage.getItem(LOCALE_KEY)
    if (raw === 'en' || raw === 'zh-CN') {
      return raw
    }
  } catch {
    // 隐私模式等忽略
  }
  return 'zh-CN'
}

function persist(locale: AppLocale): void {
  try {
    localStorage.setItem(LOCALE_KEY, locale)
  } catch {
    // 写入失败仅影响持久化
  }
  snapshot = locale
  for (const listener of listeners) {
    listener()
  }
}

function subscribe(callback: () => void): () => void {
  listeners.add(callback)
  return () => {
    listeners.delete(callback)
  }
}

function getSnapshot(): AppLocale {
  return snapshot
}

export function setLocale(locale: AppLocale): void {
  persist(locale)
}

export function currentLocale(): AppLocale {
  return snapshot
}

export function useLocale(): AppLocale {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot)
}

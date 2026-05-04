import '@testing-library/jest-dom'

function createMemoryStorage(): Storage {
  const data = new Map<string, string>()
  return {
    get length() {
      return data.size
    },
    clear() {
      data.clear()
    },
    getItem(key: string) {
      return data.has(key) ? data.get(key)! : null
    },
    key(index: number) {
      return Array.from(data.keys())[index] ?? null
    },
    removeItem(key: string) {
      data.delete(key)
    },
    setItem(key: string, value: string) {
      data.set(key, String(value))
    },
  }
}

function ensureStorage(name: 'localStorage' | 'sessionStorage') {
  const current = globalThis[name] as Storage | undefined
  if (
    current &&
    typeof current.getItem === 'function' &&
    typeof current.setItem === 'function' &&
    typeof current.removeItem === 'function' &&
    typeof current.clear === 'function'
  ) {
    return
  }
  const storage = createMemoryStorage()
  Object.defineProperty(globalThis, name, {
    configurable: true,
    value: storage,
  })
  if (typeof window !== 'undefined') {
    Object.defineProperty(window, name, {
      configurable: true,
      value: storage,
    })
  }
}

ensureStorage('localStorage')
ensureStorage('sessionStorage')

if (typeof IntersectionObserver === 'undefined') {
  globalThis.IntersectionObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords() { return [] }
    root = null
    rootMargin = ''
    thresholds = []
  }
}

if (typeof ResizeObserver === 'undefined') {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

// jsdom 不实现 scrollIntoView；MessageList 的自动滚动依赖它
if (typeof Element !== 'undefined' && !Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = function scrollIntoView() {}
}

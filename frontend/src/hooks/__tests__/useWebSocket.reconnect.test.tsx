/**
 * R5 — reconnect 后下一条消息可见 + 全程无错误态 DOM（C6-C16 两阶段侦测）
 *
 * spec Req 2 / Req 3 的端到端契约测试。
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, act, cleanup } from '@testing-library/react'
import React from 'react'
import { useChatStore } from '../../store/chat'
import { useToastStore } from '../../store/toast'
import { useAgentActivityStore } from '../../store/agentActivity'
import { useTaskProgressStore } from '../../store/taskProgress'
import type { WSMessage } from '../../types/api'

const captured: {
  onMessage: ((m: WSMessage) => void) | null
  onConnected: (() => void) | null
  onDisconnected: (() => void) | null
} = { onMessage: null, onConnected: null, onDisconnected: null }

vi.mock('../useWebSocketConnection', () => ({
  useWebSocketConnection: (opts: {
    onMessage?: (m: WSMessage) => void
    onConnected?: () => void
    onDisconnected?: () => void
  }) => {
    captured.onMessage = opts.onMessage ?? null
    captured.onConnected = opts.onConnected ?? null
    captured.onDisconnected = opts.onDisconnected ?? null
    return { connected: true, send: vi.fn() }
  },
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, defaultValue?: string) => defaultValue ?? key,
    i18n: { language: 'zh' },
  }),
  initReactI18next: { type: '3rdParty', init: () => {} },
  Trans: ({ children }: { children?: React.ReactNode }) => children,
}))

import { useWebSocket } from '../useWebSocket'
import { MessageList } from '../../components/chat/MessageList'

function Harness() {
  useWebSocket({ url: 'ws://t', sessionId: 'sid-1', enabled: true })
  const messages = useChatStore((s) => s.messages)
  return <MessageList messages={messages} />
}

// 属性层"错误态"语义：role=status / aria-live=polite 是合法中性通告
// （例如工具调用 chip "tools.invoked"），不算错误；
// sonner error toast 走 role=alert + [data-type="error"]，此处已覆盖。
const errorAttrSelector = [
  '[role="alert"]',
  '[aria-live="assertive"]',
  '[aria-invalid="true"]',
  '[data-state="error"]',
  '[data-variant="destructive"]',
  '[data-variant="error"]',
  '[data-severity="error"]',
  '[data-error="true"]',
  '[data-type="error"]',
  'dialog[open]',
].join(', ')

// 宽粗 selector 用于 MO 触发筛（包括 Tailwind 条件修饰的 token 字面），
// 最终判定靠 tokenHasDangerUtility 做 token-level 精确过滤。
// 例：shadcn Select 默认 `aria-invalid:ring-destructive/20` 只在 aria-invalid='true'
// 时生效——prefixActiveOnElement 会按 attr 激活态判定，baseline 静态 DOM 仍绿。
// R18：不再为 `!text-` / `!bg-` / `-[var(--danger` 等冗余前缀单独建 broad 项——
// 它们的 class attr 已被上游普通 utility broad（`text-red-` / `--danger` 等）
// 作为 substring 扫入；真正加值的是 `stroke/fill/caret/...` 等 utility 家族
// + `-[#` / `-[rgb` / `-[hsl` 这几条无法被普通 utility substring 顺带命中的。
const errorClassBroadSelector = [
  '[class*="text-red-"]',
  '[class*="text-rose-"]',
  '[class*="text-pink-"]',
  '[class*="text-orange-"]',
  '[class*="text-amber-"]',
  '[class*="text-yellow-"]',
  '[class*="text-danger"]',
  '[class*="text-destructive"]',
  '[class*="--danger"]',
  '[class*="--destructive"]',
  '[class*="bg-red-"]',
  '[class*="bg-rose-"]',
  '[class*="bg-orange-"]',
  '[class*="bg-amber-"]',
  '[class*="bg-yellow-"]',
  '[class*="bg-danger"]',
  '[class*="bg-destructive"]',
  '[class*="border-red-"]',
  '[class*="border-rose-"]',
  '[class*="border-orange-"]',
  '[class*="border-amber-"]',
  '[class*="border-danger"]',
  '[class*="border-destructive"]',
  '[class*="ring-red-"]',
  '[class*="ring-danger"]',
  '[class*="ring-destructive"]',
  '[class*="outline-red-"]',
  '[class*="outline-danger"]',
  '[class*="outline-destructive"]',
  // R18-B2：Tailwind 其它真实危险色 utility 家族（stroke/fill 用于 SVG icon、
  // caret/decoration/accent/placeholder 用于输入态、from/via/to 用于 gradient、
  // divide 用于子元素分隔线）——这些类名字面不含 `text-red-` / `bg-red-` 等前缀，
  // 之前 broad 完全扫不进来，属于 codex R18 点名的 silent-green。
  '[class*="stroke-red-"]',
  '[class*="stroke-rose-"]',
  '[class*="stroke-danger"]',
  '[class*="stroke-destructive"]',
  '[class*="fill-red-"]',
  '[class*="fill-rose-"]',
  '[class*="fill-danger"]',
  '[class*="fill-destructive"]',
  '[class*="caret-red-"]',
  '[class*="caret-rose-"]',
  '[class*="caret-danger"]',
  '[class*="caret-destructive"]',
  '[class*="decoration-red-"]',
  '[class*="decoration-rose-"]',
  '[class*="decoration-danger"]',
  '[class*="decoration-destructive"]',
  '[class*="accent-red-"]',
  '[class*="accent-rose-"]',
  '[class*="accent-danger"]',
  '[class*="accent-destructive"]',
  '[class*="placeholder-red-"]',
  '[class*="placeholder-rose-"]',
  '[class*="placeholder-danger"]',
  '[class*="placeholder-destructive"]',
  '[class*="from-red-"]',
  '[class*="from-rose-"]',
  '[class*="from-danger"]',
  '[class*="from-destructive"]',
  '[class*="via-red-"]',
  '[class*="via-rose-"]',
  '[class*="via-danger"]',
  '[class*="via-destructive"]',
  '[class*="to-red-"]',
  '[class*="to-rose-"]',
  '[class*="to-danger"]',
  '[class*="to-destructive"]',
  '[class*="divide-red-"]',
  '[class*="divide-rose-"]',
  '[class*="divide-danger"]',
  '[class*="divide-destructive"]',
  // R17 保留：arbitrary value 形式必须靠 `-[#` / `-[rgb` / `-[hsl` 前缀扫入
  // （普通 utility substring 对 `bg-[#ef4444]` 不顺带命中）。
  '[class*="-[#"]',
  '[class*="-[rgb"]',
  '[class*="-[hsl"]',
].join(', ')

// 联合 selector（MO 仍按旧接口使用）
const errorSignalSelector = `${errorAttrSelector}, ${errorClassBroadSelector}`

// R18-B2：扩展 utility prefix 清单至 stroke/fill/caret/decoration/accent/
// placeholder/from/via/to/divide 等真实危险色 utility 家族。所有 prefix 共用
// rose/pink/orange/amber/yellow/red/danger/destructive 色族名。
const dangerTokenRe =
  /^(?:text-red-|text-rose-|text-pink-|text-orange-|text-amber-|text-yellow-|text-danger|text-destructive|bg-red-|bg-rose-|bg-orange-|bg-amber-|bg-yellow-|bg-danger|bg-destructive|border-red-|border-rose-|border-orange-|border-amber-|border-danger|border-destructive|ring-red-|ring-danger|ring-destructive|outline-red-|outline-danger|outline-destructive|stroke-red-|stroke-rose-|stroke-danger|stroke-destructive|fill-red-|fill-rose-|fill-danger|fill-destructive|caret-red-|caret-rose-|caret-danger|caret-destructive|decoration-red-|decoration-rose-|decoration-danger|decoration-destructive|accent-red-|accent-rose-|accent-danger|accent-destructive|placeholder-red-|placeholder-rose-|placeholder-danger|placeholder-destructive|from-red-|from-rose-|from-danger|from-destructive|via-red-|via-rose-|via-danger|via-destructive|to-red-|to-rose-|to-danger|to-destructive|divide-red-|divide-rose-|divide-danger|divide-destructive|--danger|--destructive)/

// R18-B3：arbitrary value 判定改为程序化 matcher，避免穷举每种十六进制/rgb 元组
// 导致漏 `#ff0000` / `#f00` / `rgb(255,0,0)` / `hsl(...)` / `text-[color:var(--danger)]`
// 等真实危险色写法的 silent-green。判定规则：
//   1. utility prefix 必须来自扩展后的家族清单；
//   2. `[...]` 内部可选 `color:` 语法前缀；
//   3. 接受：var(--danger|--destructive)、red-dominant 十六进制（3 位或 6 位，
//      R 高 G/B 低）、red-dominant rgb()（R≥180 且 G≤100 且 B≤100）、
//      red 附近色相 hsl（0-20° 或 340-360°，或 hsl(var(--danger|--destructive))）
const ARBITRARY_UTILITY_PREFIX =
  /^(?:text|bg|border|ring|outline|stroke|fill|caret|decoration|accent|placeholder|from|via|to|divide)-\[(.+)\]$/i
function matchesArbitraryDanger(tok: string): boolean {
  const m = tok.match(ARBITRARY_UTILITY_PREFIX)
  if (!m) return false
  let inner = m[1]
  if (inner.toLowerCase().startsWith('color:')) inner = inner.slice(6)
  if (/^var\(\s*--(?:danger|destructive)\b/i.test(inner)) return true
  const hex = inner.match(/^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/)
  if (hex) {
    const h = hex[1].toLowerCase()
    if (h.length === 3) {
      const r = parseInt(h[0], 16)
      const g = parseInt(h[1], 16)
      const b = parseInt(h[2], 16)
      if (r >= 12 && g <= 6 && b <= 6) return true
    } else {
      const r = parseInt(h.slice(0, 2), 16)
      const g = parseInt(h.slice(2, 4), 16)
      const b = parseInt(h.slice(4, 6), 16)
      if (r >= 180 && g <= 100 && b <= 100) return true
    }
  }
  const rgb = inner.match(/^rgb\s*\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*\)$/i)
  if (rgb) {
    const r = +rgb[1],
      g = +rgb[2],
      b = +rgb[3]
    if (r >= 180 && g <= 100 && b <= 100) return true
  }
  const hsl = inner.match(/^hsl\s*\(\s*(var\(\s*--(?:danger|destructive)\b|-?\d+)/i)
  if (hsl) {
    if (/^var\(/i.test(hsl[1])) return true
    const hue = ((+hsl[1] % 360) + 360) % 360
    if (hue <= 20 || hue >= 340) return true
  }
  return false
}

// R18-B1：按 `[...]` 括号深度切 `:` 分隔符，尊重 arbitrary selector 里的 `:`（如
// `[&_svg:not([class*='text-'])]:text-muted-foreground`、`text-[color:var(--danger)]`）。
function splitAtTopColons(token: string): string[] {
  const parts: string[] = []
  let depth = 0
  let start = 0
  for (let i = 0; i < token.length; i++) {
    const c = token[i]
    if (c === '[') depth++
    else if (c === ']') depth--
    else if (c === ':' && depth === 0) {
      parts.push(token.slice(start, i))
      start = i + 1
    }
  }
  parts.push(token.slice(start))
  return parts
}

// 纯媒体查询 / 伪类 / 状态类 prefix——jsdom 静态 DOM 下永远 inactive。
// 注意这里故意不含 `aria-*` / `data-*`（由下面 prefixActiveOnElement 按 attr 逐元素判定激活）。
const INACTIVE_STATIC_PREFIX =
  /^(?:hover|focus|focus-visible|focus-within|active|visited|target|group-hover|group-focus|peer-hover|peer-focus|before|after|first|last|only|odd|even|empty|disabled|enabled|checked|required|optional|placeholder-shown|file|selection|marker|dark|light|print|motion-safe|motion-reduce|portrait|landscape|sm|md|lg|xl|2xl|3xl|container|rtl|ltr)$/

function prefixActiveOnElement(el: Element, prefix: string): boolean {
  if (INACTIVE_STATIC_PREFIX.test(prefix)) return false
  // group-/peer- 这类依赖祖先/兄弟状态的条件，jsdom 下难精确求值——保守判 inactive
  if (/^(?:group|peer)-/.test(prefix)) return false
  // aria-<name> 简写（aria-invalid / aria-busy / aria-disabled 等）
  let m = prefix.match(/^aria-([\w-]+)$/)
  if (m) return el.getAttribute(`aria-${m[1]}`) === 'true'
  // aria-[<name>=<value>] 全写
  m = prefix.match(/^aria-\[([\w-]+)=([^\]]+)\]$/)
  if (m) return el.getAttribute(`aria-${m[1]}`) === m[2]
  // data-<name> 简写（任意值即激活，与 Tailwind 默认 behavior 一致）
  m = prefix.match(/^data-([\w-]+)$/)
  if (m) return el.hasAttribute(`data-${m[1]}`)
  // data-[<name>=<value>] 全写
  m = prefix.match(/^data-\[([\w-]+)=([^\]]+)\]$/)
  if (m) return el.getAttribute(`data-${m[1]}`) === m[2]
  // `[&_...]` 任意 selector variant——保守判 inactive（jsdom 无法跨元素求值）
  if (prefix.startsWith('[')) return false
  // 兜底：未识别 prefix 保守判 active（防止新 Tailwind 变体 silent-green）
  return true
}

function tokenHasDangerUtility(classAttr: string, el: Element): boolean {
  for (const rawTok of classAttr.split(/\s+/)) {
    if (!rawTok) continue
    const parts = splitAtTopColons(rawTok)
    const suffix = parts[parts.length - 1]
    const tok = suffix.replace(/^!/, '')
    const isDanger = dangerTokenRe.test(tok) || matchesArbitraryDanger(tok)
    if (!isDanger) continue
    if (parts.length === 1) return true
    let allActive = true
    for (let i = 0; i < parts.length - 1; i++) {
      if (!prefixActiveOnElement(el, parts[i])) {
        allActive = false
        break
      }
    }
    if (allActive) return true
  }
  return false
}
function isActualErrorEl(el: Element): boolean {
  if (el.matches?.(errorAttrSelector)) return true
  return tokenHasDangerUtility(el.getAttribute?.('class') || '', el)
}
function filterActualErrorEls(els: Element[]): Element[] {
  return els.filter(isActualErrorEl)
}
const inlineStyleRe =
  /(?:--danger|--destructive|color:\s*(?:red|#[fF][0-9a-fA-F]{2}|rgb\([^)]*(?:2[0-9]{2}|1[89][0-9])\b))/i

describe('R5 reconnect 可见性 + 无错误态 DOM', () => {
  async function flushRaf() {
    await act(async () => {
      await new Promise<void>((r) => requestAnimationFrame(() => r()))
    })
  }

  beforeEach(() => {
    captured.onMessage = null
    captured.onConnected = null
    captured.onDisconnected = null
    useChatStore.setState({
      messages: [],
      currentSessionId: null,
      streamingMessageId: null,
      streaming: false,
      sending: false,
      agentStatus: null,
      inlineApprovals: [],
      toolCallStatuses: {},
      toolCallStartTimes: {},
      error: null,
    })
    useToastStore.setState({ toasts: [] })
    useAgentActivityStore.setState({ sessionStatus: {} })
    useTaskProgressStore.setState({ activeGroups: new Map() })
  })

  afterEach(() => cleanup())

  it('disconnect → connect → partial 可见，且窗口内无错误态 DOM', async () => {
    // === 1. harness 预置（顺序严格：在 C6-C15 baseline snapshot 之前）===
    //   preset 消息顺序：[preset-plain-assistant-2, preset-assistant-1, preset-tool-1]
    //   tool 置尾避免 ensureAssistantMessage 复用 last assistant
    useChatStore.setState({
      currentSessionId: 'sid-1',
      streamingMessageId: null,
      messages: [
        {
          timestamp: 'preset-plain-assistant-2',
          role: 'assistant',
          content: '历史回复 B',
          is_error: false,
        },
        {
          timestamp: 'preset-assistant-1',
          role: 'assistant',
          content: '历史回复 A',
          is_error: false,
          tool_calls: [{ id: 'tc-1', name: 'search', arguments: '{"q":"x"}' }],
        },
        {
          timestamp: 'preset-tool-1',
          role: 'tool',
          tool_call_id: 'tc-1',
          content: '{"ok":true}',
          is_error: false,
        },
      ],
      toolCallStatuses: { 'tc-1': { id: 'tc-1', name: 'search', status: 'running' } },
    })

    useTaskProgressStore.getState().setTaskGroup({
      group_id: 'g1',
      tasks: [{ id: 't1', status: 'running', error: undefined, agent_id: 'a1' }],
    })

    const presetSnapshot = [
      {
        timestamp: 'preset-assistant-1',
        role: 'assistant' as const,
        tool_call_id: undefined,
        content: '历史回复 A',
        tool_calls: [{ id: 'tc-1', name: 'search', arguments: '{"q":"x"}' }] as
          | { id: string; name: string; arguments: string }[]
          | undefined,
      },
      {
        timestamp: 'preset-tool-1',
        role: 'tool' as const,
        tool_call_id: 'tc-1',
        content: '{"ok":true}',
        tool_calls: undefined,
      },
      {
        timestamp: 'preset-plain-assistant-2',
        role: 'assistant' as const,
        tool_call_id: undefined,
        content: '历史回复 B',
        tool_calls: undefined,
      },
    ]

    render(<Harness />)

    // === 2. baseline 硬 sanity（MutationObserver 启动之前）===
    const baselineErrEls = filterActualErrorEls(
      Array.from(document.querySelectorAll(errorSignalSelector))
    )
    const baselineInline = Array.from(document.querySelectorAll('[style]')).filter((el) =>
      inlineStyleRe.test(el.getAttribute('style') || '')
    )
    expect(baselineErrEls.length).toBe(0)
    expect(baselineInline.length).toBe(0)

    // === 3. 采集各 store baseline ===
    const toastBefore = useToastStore.getState().toasts.length
    const messagesLengthBefore = useChatStore.getState().messages.length
    const isErrorCountBefore = useChatStore
      .getState()
      .messages.filter((m) => m.is_error === true).length
    const toolErrorContentBefore = useChatStore
      .getState()
      .messages.filter(
        (m) =>
          m.role === 'tool' &&
          /^(tool error:|tool execution failed:|tool '.*' .*not allowed|ToolBridge not initialized|\[工具调用被中断|\[工具执行失败)/i.test(
            m.content || ''
          )
      ).length
    const approvalsBefore = useChatStore.getState().inlineApprovals.length
    const groupsBefore = new Map(useTaskProgressStore.getState().activeGroups)
    const taskErrorCountBefore = [
      ...useTaskProgressStore.getState().activeGroups.values(),
    ]
      .flatMap((g) => g.tasks)
      .filter((t) => !!t.error).length
    const agentStatusBefore = useAgentActivityStore.getState().sessionStatus['sid-1']
    const toolErrorStatusBefore = Object.values(useChatStore.getState().toolCallStatuses).filter(
      (s) => s?.status === 'error'
    ).length

    // === 4. 启动 MutationObserver Stage 1 ===
    const transientHits: { kind: string; summary: string }[] = []
    const recordElement = (el: Element, kind: string) => {
      if (isActualErrorEl(el)) {
        transientHits.push({ kind, summary: `${kind}:selector:${el.outerHTML.slice(0, 120)}` })
      }
      const style = el.getAttribute?.('style') || ''
      if (inlineStyleRe.test(style)) {
        transientHits.push({ kind, summary: `${kind}:inline-style:${el.outerHTML.slice(0, 120)}` })
      }
      el.querySelectorAll?.(errorSignalSelector).forEach((child) => {
        if (isActualErrorEl(child)) {
          transientHits.push({ kind: `${kind}:descendant`, summary: child.outerHTML.slice(0, 120) })
        }
      })
    }
    const mo = new MutationObserver((muts) => {
      for (const m of muts) {
        m.addedNodes.forEach((n) => {
          if (n instanceof Element) recordElement(n, 'added')
        })
        if (m.type === 'attributes' && m.target instanceof Element) {
          recordElement(m.target, `attr:${m.attributeName}`)
        }
      }
    })
    mo.observe(document.body, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: [
        'class',
        'style',
        'role',
        'aria-live',
        'aria-invalid',
        // R18-B1：条件修饰触发器——aria-busy / aria-disabled / data-state 等 attr
        // 翻转会激活 `aria-busy:text-red-500` / `data-[state=offline]:bg-red-500`
        // 等条件类的真实渲染。attr 层变更本身由 MO 观察到后再跑 isActualErrorEl，
        // 元素 attr+class 组合判 active 即 flag。
        'aria-busy',
        'aria-disabled',
        'aria-hidden',
        'data-state',
        'data-variant',
        'data-severity',
        'data-error',
        'data-type',
        'data-status',
        'open',
      ],
    })

    // === 5. disconnect → connect → partial ===
    await act(async () => {
      captured.onDisconnected?.()
    })

    // 断言 A（R3 subset）
    expect(useChatStore.getState().currentSessionId).toBe('sid-1')

    await act(async () => {
      captured.onConnected?.()
    })

    await act(async () => {
      captured.onMessage!({
        type: 'message',
        payload: {
          session_id: 'sid-1',
          partial: true,
          content: '重连后首帧',
          role: 'assistant',
        },
      } as WSMessage)
    })
    await flushRaf()

    // 断言 B：partial 可见
    await screen.findByText(/重连后首帧/)

    // === 6. 断言 C6-C16（soft，独立 attribution）===
    // C6：toast 计数不变
    expect.soft(useToastStore.getState().toasts.length).toBe(toastBefore)

    // C7a：messages 长度增量 ∈ {0, 1}
    const delta = useChatStore.getState().messages.length - messagesLengthBefore
    expect.soft(delta).toBeGreaterThanOrEqual(0)
    expect.soft(delta).toBeLessThanOrEqual(1)

    // C7b：新增的那条必须 assistant 且非 is_error
    // C7c：新增的那条必须是 partial 定型出来的那条（content 包含首帧文本），
    //      反制"attacker 先插入一条中性 assistant 再让 partial 复用"的假绿——
    //      若有人在 reconnect 窗口追加纯空白占位 assistant，content 不会命中首帧断言。
    if (delta === 1) {
      const newMsg = useChatStore.getState().messages[useChatStore.getState().messages.length - 1]
      expect.soft(newMsg.role, 'C7b new-msg role').toBe('assistant')
      expect.soft(newMsg.is_error === true, 'C7b new-msg is_error').toBe(false)
      expect.soft(newMsg.content || '', 'C7c new-msg 必须是 partial 首帧').toContain('重连后首帧')
    }

    // C8：inlineApprovals 不变
    expect.soft(useChatStore.getState().inlineApprovals.length).toBe(approvalsBefore)

    // C9：activeGroups size 不变、不引入新 failed
    const groupsAfter = useTaskProgressStore.getState().activeGroups
    expect.soft(groupsAfter.size).toBe(groupsBefore.size)
    const newFailed = [...groupsAfter.values()].flatMap((g) =>
      g.tasks.filter((t) => {
        if (t.status !== 'failed') return false
        const beforeG = groupsBefore.get(g.groupId)
        const beforeT = beforeG?.tasks.find((bt) => bt.id === t.id)
        return beforeT?.status !== 'failed'
      })
    )
    expect.soft(newFailed.length).toBe(0)

    // C10：sessionStatus['sid-1'] 不进入 error
    const agentAfter = useAgentActivityStore.getState().sessionStatus['sid-1']
    expect.soft(agentAfter).not.toBe('error')
    // 记录 baselineAgent 让 attribution 明显
    expect.soft(agentStatusBefore ?? null).not.toBe('error')

    // C11：toolCallStatuses error 态计数不变
    const toolErrorAfter = Object.values(useChatStore.getState().toolCallStatuses).filter(
      (s) => s?.status === 'error'
    ).length
    expect.soft(toolErrorAfter).toBe(toolErrorStatusBefore)

    // C12：is_error 翻真计数不变
    const isErrorAfter = useChatStore
      .getState()
      .messages.filter((m) => m.is_error === true).length
    expect.soft(isErrorAfter).toBe(isErrorCountBefore)

    // C13：tool 错误前缀计数不变
    const toolErrorContentAfter = useChatStore
      .getState()
      .messages.filter(
        (m) =>
          m.role === 'tool' &&
          /^(tool error:|tool execution failed:|tool '.*' .*not allowed|ToolBridge not initialized|\[工具调用被中断|\[工具执行失败)/i.test(
            m.content || ''
          )
      ).length
    expect.soft(toolErrorContentAfter).toBe(toolErrorContentBefore)

    // C14：任务 error 非空计数不变
    const taskErrorCountAfter = [
      ...useTaskProgressStore.getState().activeGroups.values(),
    ]
      .flatMap((g) => g.tasks)
      .filter((t) => !!t.error).length
    expect.soft(taskErrorCountAfter).toBe(taskErrorCountBefore)

    // C15：三元组冻结（a/b/c/d）
    const msgs = useChatStore.getState().messages
    for (const p of presetSnapshot) {
      const matches = msgs.filter(
        (m) =>
          m.timestamp === p.timestamp &&
          m.role === p.role &&
          (m.tool_call_id || undefined) === (p.tool_call_id || undefined)
      )
      // C15a：唯一存在
      expect.soft(matches.length, `C15a ${p.timestamp}`).toBe(1)
      if (matches.length === 1) {
        // C15b：content 字面冻结
        expect.soft(matches[0].content, `C15b ${p.timestamp}`).toBe(p.content)
        // C15c：content 类型是字符串
        expect.soft(typeof matches[0].content, `C15c ${p.timestamp}`).toBe('string')
        // C15d：tool_calls 深度冻结
        expect
          .soft(JSON.stringify(matches[0].tool_calls), `C15d ${p.timestamp}`)
          .toBe(JSON.stringify(p.tool_calls))
      }
    }

    // === 7. C16 Stage 1（MutationObserver）结果 ===
    mo.disconnect()
    expect.soft(transientHits).toEqual([])

    // === 8. C16 Stage 2（final absolute 扫描）===
    const finalErrEls = filterActualErrorEls(
      Array.from(document.querySelectorAll(errorSignalSelector))
    )
    const finalInline = Array.from(document.querySelectorAll('[style]')).filter((el) =>
      inlineStyleRe.test(el.getAttribute('style') || '')
    )
    expect.soft(finalErrEls.length).toBe(0)
    expect.soft(finalInline.length).toBe(0)

    // 辅助文案诊断（非契约主力）
    const chatText = document.body.textContent || ''
    expect.soft(/连接中断|重连失败|reconnect failed/.test(chatText)).toBe(false)
    expect.soft(screen.queryAllByRole('alert').length).toBe(0)
    expect.soft(useChatStore.getState().error).toBeFalsy()
  })

  it('currentSessionId 为空时仍按订阅 sessionId 过滤聊天消息', async () => {
    render(<Harness />)
    await act(async () => {
      useChatStore.setState({ currentSessionId: null, messages: [] })
    })

    await act(async () => {
      captured.onMessage!({
        type: 'message',
        payload: {
          session_id: 'sid-other',
          partial: false,
          role: 'assistant',
          content: '不应显示的跨会话消息',
        },
      } as WSMessage)
    })

    expect(useChatStore.getState().messages).toHaveLength(0)
    expect(screen.queryByText(/不应显示的跨会话消息/)).toBeNull()

    await act(async () => {
      captured.onMessage!({
        type: 'message',
        payload: {
          session_id: 'sid-1',
          partial: false,
          role: 'assistant',
          content: '应显示的当前会话消息',
        },
      } as WSMessage)
    })

    expect(useChatStore.getState().messages).toHaveLength(1)
    expect(screen.getByText(/应显示的当前会话消息/)).toBeInTheDocument()
  })
})

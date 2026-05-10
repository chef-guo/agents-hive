import { useEffect, useRef, useCallback, useState, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { CheckCircle, XCircle, Brain, ChevronDown } from 'lucide-react';
import { MessageBubble } from './MessageBubble';
import { MessageBubbleBoundary } from './MessageBubbleBoundary';
import { ApprovalCard } from '../hitl/ApprovalCard';
import { TaskProgressPanel } from './TaskProgressPanel';
import { ClawIcon } from './shared';
import { useChatStore } from '../../store/chat';
import { useHITLSubmit } from '../../hooks/useHITLSubmit';
import type { Message, InputRequest, InputResponse } from '../../types/api';
import { getToolDisplayName } from '../../utils/toolName';

// 从审批请求中提取操作摘要
function extractApprovalSummary(req: InputRequest): string {
  if (req.type !== 'permission' || !req.data) return req.prompt || '';
  const data = req.data as Record<string, unknown>;
  const keyFields: Record<string, string[]> = {
    bash: ['command'],
    edit: ['file_path'],
    write_file: ['file_path'],
    read_file: ['file_path'],
    glob: ['pattern', 'path'],
    grep: ['pattern', 'path'],
    skill: ['name'],
  };
  const fields = keyFields[req.tool_name || ''] || ['command', 'file_path', 'path'];
  for (const field of fields) {
    const val = data[field];
    if (val != null && val !== '') {
      const s = String(val);
      return s.length > 80 ? s.slice(0, 77) + '...' : s;
    }
  }
  for (const [, v] of Object.entries(data)) {
    if (v != null && v !== '') {
      const s = String(v);
      return s.length > 80 ? s.slice(0, 77) + '...' : s;
    }
  }
  return req.prompt || '';
}

interface Props {
  messages: Message[];
  loading?: boolean;
  streamingStatus?: string | null;
  onRegenerate?: () => void;
}

export function MessageList({ messages, loading, streamingStatus, onRegenerate }: Props) {
  const { t } = useTranslation();
  const inlineApprovals = useChatStore((s) => s.inlineApprovals);
  const { submitApproval } = useHITLSubmit();
  const endRef = useRef<HTMLDivElement>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const rafRef = useRef<number>(0);
  const prevApprovalCountRef = useRef(0);
  // 已处理的审批记录（保留详情用于展示）
  interface ProcessedApproval {
    action: string;       // 'approve' | 'reject'
    toolName?: string;    // 工具名
    summary: string;      // 操作摘要
    timestamp: string;    // 锚定到消息的 timestamp（不受 sorted insert 影响）
  }
  const [processedApprovals, setProcessedApprovals] = useState<Map<string, ProcessedApproval>>(new Map());
  const [showScrollBtn, setShowScrollBtn] = useState(false);

  // 用户是否主动向上滚动（为 false 时自动跟随底部）
  const userScrolledUp = useRef(false);
  // 上一次已知的 scrollHeight，用于区分"用户滚动"和"内容增长导致的滚动偏移"
  const prevScrollHeight = useRef(0);
  // 程序化滚动标记，防止 smooth 动画期间 handleScroll 误判为用户向上滚动
  const programmaticScroll = useRef(false);

  // 监听滚动事件，判断用户是否主动向上滚动
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    const handleScroll = () => {
      const { scrollTop, scrollHeight, clientHeight } = container;
      const distanceFromBottom = scrollHeight - scrollTop - clientHeight;
      // 如果用户滚动到距底部 150px 以内，视为回到底部
      if (distanceFromBottom < 150) {
        userScrolledUp.current = false;
      } else if (!programmaticScroll.current && scrollHeight === prevScrollHeight.current) {
        // scrollHeight 没变但距离变远了，且非程序化滚动 → 用户主动向上滚动
        userScrolledUp.current = true;
      }
      prevScrollHeight.current = scrollHeight;
    };

    container.addEventListener('scroll', handleScroll, { passive: true });
    return () => container.removeEventListener('scroll', handleScroll);
  }, []);

  // 用 IntersectionObserver 监听底部元素可见性，控制"滚到底部"按钮
  useEffect(() => {
    const el = endRef.current;
    const container = containerRef.current;
    if (!el || !container) return;
    const observer = new IntersectionObserver(
      ([entry]) => setShowScrollBtn(!entry.isIntersecting),
      { root: container, threshold: 0 }
    );
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  // 滚动到底部
  const scrollToBottom = useCallback((instant?: boolean) => {
    if (rafRef.current) cancelAnimationFrame(rafRef.current);
    programmaticScroll.current = true;
    rafRef.current = requestAnimationFrame(() => {
      endRef.current?.scrollIntoView({ behavior: instant ? 'instant' : 'smooth' });
      // instant 模式立即清除标记；smooth 模式延迟清除（等动画完成）
      if (instant) {
        programmaticScroll.current = false;
      } else {
        setTimeout(() => { programmaticScroll.current = false; }, 400);
      }
      rafRef.current = 0;
    });
  }, []);

  // 用户发送新消息时（messages 末尾是 user），强制回到底部
  useEffect(() => {
    if (messages.length > 0 && messages[messages.length - 1].role === 'user') {
      userScrolledUp.current = false;
      scrollToBottom(true);
    }
  }, [messages.length, messages, scrollToBottom]);

  // 内容变化时自动滚动（除非用户主动向上滚动了）
  useEffect(() => {
    if (userScrolledUp.current) return;

    const timer = setTimeout(() => {
      // 流式/加载中使用 instant 避免多个 smooth 动画互相打断导致抖动
      scrollToBottom(!!(loading || streamingStatus));
    }, 0);

    return () => {
      clearTimeout(timer);
      if (rafRef.current) cancelAnimationFrame(rafRef.current);
    };
  }, [messages, loading, streamingStatus, scrollToBottom]);

  // 新审批出现时才滚到底部（审批被处理/移除时不触发，避免多余的 smooth 动画）
  useEffect(() => {
    if (inlineApprovals.length > prevApprovalCountRef.current && !userScrolledUp.current) {
      scrollToBottom();
    }
    prevApprovalCountRef.current = inlineApprovals.length;
  }, [inlineApprovals.length, scrollToBottom]);

  // 预处理：将 role='tool' 消息的 content 按 tool_call_id 关联到 ToolCallCard
  // 注意：必须在所有条件返回之前调用，保证 hooks 调用顺序一致
  const { toolResults, toolErrors, toolNames, assistantToolCallIds } = useMemo(() => {
    const results = new Map<string, string>();
    const errors = new Map<string, boolean>();
    const names = new Map<string, string>();
    const assistantIds = new Set<string>();
    for (const msg of messages) {
      if (msg.role === 'tool' && msg.tool_call_id && msg.content) {
        results.set(msg.tool_call_id, msg.content);
        if (msg.is_error) {
          errors.set(msg.tool_call_id, true);
        }
      }
      if (msg.role === 'assistant' && msg.tool_calls) {
        for (const tc of msg.tool_calls) {
          names.set(tc.id, tc.name);
          assistantIds.add(tc.id);
        }
      }
    }
    return { toolResults: results, toolErrors: errors, toolNames: names, assistantToolCallIds: assistantIds };
  }, [messages]);

  // 空状态
  if (messages.length === 0) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <div className="text-center">
          <div className="w-12 h-12 mx-auto mb-4 rounded-full bg-[var(--accent-600)] dark:bg-[var(--accent-500)] flex items-center justify-center">
            <ClawIcon className="w-7 h-7" />
          </div>
          <h2 className="text-lg font-semibold text-[var(--text-primary)]">{t('chat.welcomeTitle')}</h2>
          <div className="text-[var(--text-secondary)] text-sm mt-1.5">{t('chat.emptyHint')}</div>
        </div>
      </div>
    );
  }

  // 找到最后一条用户消息的索引（重新生成按钮显示在用户消息下方）
  let lastUserIdx = -1;
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].role === 'user') {
      lastUserIdx = i;
      break;
    }
  }

  return (
    <div className="flex-1 relative" style={{ minHeight: 0, overflow: 'hidden' }}>
    <div ref={containerRef} className="h-full overflow-y-auto" style={{ minHeight: 0 }}>
      <div className="max-w-4xl mx-auto py-2">
        {messages.map((msg, i) => {
          // 跳过 tool 消息的独立渲染（结果已整合到 ToolCallCard 中）
          if (msg.role === 'tool' && msg.tool_call_id && assistantToolCallIds.has(msg.tool_call_id)) {
            return null;
          }

          const prevRole = i > 0 ? messages[i - 1].role : null;
          const showRole = msg.role !== prevRole;

          // 收集应显示在该消息之后的审批卡片和已处理提示
          const approvalsHere = inlineApprovals.filter((r) => r.afterMessageTimestamp === msg.timestamp);
          const processedHere = Array.from(processedApprovals.entries()).filter(
            ([, info]) => info.timestamp === msg.timestamp
          );

          return (
            <div key={msg.tool_call_id ? `${msg.timestamp}-${msg.tool_call_id}-${i}` : `${msg.timestamp || 'msg'}-${i}`}>
              <MessageBubbleBoundary
                messageTimestamp={msg.timestamp}
                messageRole={msg.role}
                messageContentPreview={typeof msg.content === 'string' ? msg.content.slice(0, 120) : undefined}
              >
                <MessageBubble
                  message={msg}
                  showRole={showRole}
                  isLast={i === lastUserIdx}
                  onRegenerate={onRegenerate}
                  toolResults={toolResults}
                  toolErrors={toolErrors}
                  toolNames={toolNames}
                />
              </MessageBubbleBoundary>
              {/* 内联审批卡片：紧跟对应的消息渲染 */}
              {approvalsHere.map((req) => (
                <div key={req.id} className="px-4 pt-2 pb-1">
                  <div className="pl-11">
                    <ApprovalCard
                      request={req}
                      onSubmit={(resp: InputResponse) => {
                        // 提取操作摘要
                        const summary = extractApprovalSummary(req);
                        useChatStore.getState().removeInlineApproval(req.id);
                        setProcessedApprovals((m) => new Map(m).set(req.id, {
                          action: resp.action,
                          toolName: req.tool_name,
                          summary,
                          timestamp: msg.timestamp || '',
                        }));
                        submitApproval(req, resp);
                      }}
                    />
                  </div>
                </div>
              ))}
              {/* 已处理审批的状态提示（保留工具名和操作详情） */}
              {processedHere.map(([id, info]) => {
                const isRejected = info.action === 'reject' || info.action === 'cancel';
                const actionLabel = isRejected ? t('hitl.reject')
                  : info.action === 'approve' ? t('hitl.approve')
                  : t('hitl.approve'); // proceed, modify 等均视为正向操作
                return (
                  <div key={`processed-${id}`} className="px-4 pt-1 pb-1">
                    <div className="pl-11">
                      <div className={`flex items-center gap-2 text-sm py-1.5 px-3 rounded-lg w-fit ${
                        isRejected
                          ? 'text-red-600 dark:text-red-400 bg-red-50 dark:bg-red-900/20'
                          : 'text-emerald-600 dark:text-emerald-400 bg-emerald-50 dark:bg-emerald-900/20'
                      }`}>
                        {isRejected
                          ? <XCircle className="w-4 h-4 shrink-0" />
                          : <CheckCircle className="w-4 h-4 shrink-0" />}
                        <span>{actionLabel}</span>
                        {info.toolName && (
                          <span className="px-1.5 py-0.5 text-xs font-mono bg-black/5 dark:bg-white/10 rounded">
                            {getToolDisplayName(info.toolName, t)}
                          </span>
                        )}
                        {info.summary && (
                          <span className="text-xs font-mono text-[var(--text-secondary)] truncate max-w-xs" title={info.summary}>
                            {info.summary}
                          </span>
                        )}
                      </div>
                    </div>
                  </div>
                );
              })}
            </div>
          );
        })}

        {/* 任务进度面板 */}
        <TaskProgressPanel />

        {/* 流式状态 / 加载指示器 */}
        {(loading || streamingStatus) && (
          <div className="px-4 pt-2 pb-1">
            <div className="max-w-4xl mx-auto">
              <div className="flex gap-3">
                <div className="w-7 h-7 rounded-full bg-[var(--accent-600)] dark:bg-[var(--accent-500)] flex items-center justify-center shrink-0">
                  <ClawIcon className="w-4 h-4" />
                </div>
                <div className="bg-[var(--bg-card)] border border-[var(--border-color)] rounded-xl px-3 py-2 flex items-center gap-2 text-sm text-[var(--text-secondary)] shadow-sm">
                  <Brain className="w-3.5 h-3.5 text-[var(--accent-600)] dark:text-[var(--accent-300)] thinking-pulse" />
                  <span className="inline-flex gap-1">
                    <span className="w-1.5 h-1.5 rounded-full bg-[var(--accent-500)] thinking-dot" />
                    <span className="w-1.5 h-1.5 rounded-full bg-[var(--accent-500)] thinking-dot" />
                    <span className="w-1.5 h-1.5 rounded-full bg-[var(--accent-500)] thinking-dot" />
                  </span>
                  {streamingStatus && (
                    <span className="text-xs">
                      {streamingStatus === 'thinking' && t('chat.thinking')}
                      {streamingStatus === 'tool_calling' && t('chat.callingTools')}
                      {streamingStatus === 'completed' && t('chat.completed')}
                    </span>
                  )}
                </div>
              </div>
            </div>
          </div>
        )}

        <div ref={endRef} />
      </div>
    </div>
      {/* 滚动到底部浮动按钮 */}
      {showScrollBtn && (
        <button
          onClick={() => { userScrolledUp.current = false; scrollToBottom(); }}
          className="absolute bottom-4 left-1/2 -translate-x-1/2 p-2 rounded-full bg-[var(--bg-primary)] shadow-lg border border-[var(--border-color)] text-[var(--text-secondary)] hover:shadow-xl transition-all duration-200 hover:scale-105 z-10"
        >
          <ChevronDown className="w-5 h-5" />
        </button>
      )}
    </div>
  );
}

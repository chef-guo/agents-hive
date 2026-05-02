import { useEffect, useCallback, useMemo } from 'react';
import { useParams, useNavigate, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { ArrowLeft, X } from 'lucide-react';
import { useSessionStore } from '../store/session';
import { useChatStore } from '../store/chat';
import { useHITLStore } from '../store/hitl';
import { useCanvasStore } from '../store/canvas';
import { useNodeClient } from '../hooks/useNodeClient';
import { useHeaderStore } from '../store/header';
import { MessageList } from '../components/chat/MessageList';
import { ChatInput } from '../components/chat/ChatInput';
import { CanvasPanel } from '../components/canvas/CanvasPanel';
import { useTaskProgressStore } from '../store/taskProgress';
import { shouldShowTodosPanel, useTodosStore } from '../store/todos';
import { TodosList } from '../components/todos/TodosList';

export function Chat() {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const location = useLocation();
  const client = useNodeClient();
  const pendingMessage = (location.state as { pendingMessage?: string } | null)?.pendingMessage;

  const currentSession = useSessionStore((s) => s.currentSession);
  const fetchSession = useSessionStore((s) => s.fetchSession);
  const clearSessionApi = useSessionStore((s) => s.clearSession);
  const sessions = useSessionStore((s) => s.sessions);

  const messages = useChatStore((s) => s.messages);
  const sending = useChatStore((s) => s.sending);
  const streaming = useChatStore((s) => s.streaming);
  const agentStatus = useChatStore((s) => s.agentStatus);
  const error = useChatStore((s) => s.error);
  const sendMessage = useChatStore((s) => s.sendMessage);
  const clearError = useChatStore((s) => s.clearError);
  const loadMessages = useChatStore((s) => s.loadMessages);
  const clearMessages = useChatStore((s) => s.clearMessages);
  const loadModels = useChatStore((s) => s.loadModels);
  const stopTask = useChatStore((s) => s.stopTask);

  const updateSessionName = useSessionStore((s) => s.updateSessionName);

  const canvasOpen = useCanvasStore((s) => s.open);
  const todosSnapshot = useTodosStore((s) => s.snapshot);
  const todosPanelOpen = shouldShowTodosPanel(todosSnapshot);
  const workspaceOpen = canvasOpen || todosPanelOpen;
  const workspaceWidthClass = canvasOpen ? 'md:w-1/2' : 'md:w-80';

  useEffect(() => {
    if (id) {
      useCanvasStore.getState().closeAll(); // 切换会话时清理 Canvas
      clearMessages();
      useTodosStore.getState().clear();
      // 切换会话时清理进度
      useTaskProgressStore.getState().clear();
      fetchSession(client, id);
      useTodosStore.getState().loadSnapshot(client, id);
      // 先加载消息，再拉取待处理权限请求（确保锚定位置正确）
      loadMessages(client, id, 100).then(() => {
        useHITLStore.getState().fetchPending(client, id);

        // 从着陆页带过来的待发消息，自动发送
        if (pendingMessage) {
          // 清除 state 避免刷新后重复发送
          window.history.replaceState({}, '');
          sendMessage(client, id, pendingMessage);
          // 用消息内容自动命名会话
          const title = pendingMessage.trim().slice(0, 20);
          if (title) {
            client.updateSession(id, { name: title }).catch(() => {});
            updateSessionName(id, title);
          }
        }
      });
      loadModels(client);
    }
    return () => {
      clearMessages();
      useCanvasStore.getState().closeAll();
      useTaskProgressStore.getState().clear();
      useTodosStore.getState().clear();
    };
  }, [id, client, fetchSession, loadMessages, clearMessages, loadModels, pendingMessage, sendMessage, updateSessionName]);

  // 会话被删除后自动跳转回会话列表
  useEffect(() => {
    if (!id) return;
    // currentSession 被 deleteSession 设为 null，且该 id 已不在列表中 → 已删除
    if (!currentSession && sessions.length > 0 && !sessions.some((s) => s.id === id)) {
      navigate('/');
    }
  }, [id, currentSession, sessions, navigate]);

  const handleSend = useCallback((content: string, options?: { attachments?: import('../types/api').FileAttachment[]; deepThinking?: boolean }) => {
    if (id) {
      // 发送第一条消息时，用消息内容自动重命名会话
      if (messages.length === 0) {
        const title = content.trim().slice(0, 20);
        if (title) {
          client.updateSession(id, { name: title }).catch(() => {});
          updateSessionName(id, title);
        }
      }
      sendMessage(client, id, content, options);
    }
  }, [id, client, sendMessage, messages.length, updateSessionName]);

  const handleClear = useCallback(async () => {
    if (id && confirm(t('chat.clearConfirm'))) {
      try {
        await clearSessionApi(client, id);
        clearMessages();
      } catch {
        useChatStore.setState({ error: t('chat.clearFailed', '清空会话失败，请重试') });
      }
    }
  }, [id, t, client, clearSessionApi, clearMessages]);

  const handleRegenerate = useCallback(async () => {
    if (!id) return;

    // 乐观 UI：找最后一条用户消息，保留它，删掉其后的所有内容（含 tool call / tool result 等）
    const lastUserMsgIdx = [...messages].map((m, i) => ({ role: m.role, i })).reverse().find(m => m.role === 'user')?.i;
    if (lastUserMsgIdx !== undefined) {
      useChatStore.getState().setMessages(messages.slice(0, lastUserMsgIdx + 1));
    }

    // 立即显示"思考中"状态，避免等待 WebSocket 事件的时间窗口内无反馈
    useChatStore.setState({ streaming: true, agentStatus: 'thinking' });

    // 后端统一完成：回滚旧数据 + 重新生成 AI 回复（通过 WebSocket 流式返回）
    try {
      await client.regenerateMessage(id);
    } catch {
      // 清理 streaming 状态，避免卡在"思考中"
      useChatStore.setState({ streaming: false, streamingMessageId: null, agentStatus: null });
    }
  }, [id, messages, client]);

  const handleStop = useCallback(() => {
    if (id) stopTask(client, id);
  }, [id, client, stopTask]);

  // 注入全局 Header 的 slots（返回按钮 + 会话名 + 消息统计）
  const setSlots = useHeaderStore((s) => s.setSlots);
  const clearSlots = useHeaderStore((s) => s.clearSlots);
  const sessionName = messages.length === 0
    ? t('sessions.newSession', '新会话')
    : (currentSession?.name || id?.slice(0, 8));

  // 从消息列表实时累加 completion tokens（不用 stale 的 currentSession.total_tokens）
  const totalTokens = useMemo(() => messages.reduce((sum, m) => sum + (m.usage?.output_tokens ?? 0), 0), [messages]);

  useEffect(() => {
    setSlots({
      leftExtra: (
        <button
          onClick={() => navigate('/sessions')}
          className="p-1.5 rounded-lg text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-hover)] transition-colors"
          aria-label={t('chat.back')}
        >
          <ArrowLeft className="w-4 h-4" />
        </button>
      ),
      centerOverride: (
        <span className="text-sm font-semibold text-[var(--text-primary)] truncate max-w-xs pointer-events-auto">
          {sessionName}
        </span>
      ),
      rightExtra: (
        <div className="flex items-center gap-3 mr-1">
          {currentSession && (
            <span className="text-xs text-[var(--text-secondary)] hidden sm:inline">
              {currentSession.message_count} {t('sessions.messages')} | {totalTokens} {t('sessions.tokens')}
            </span>
          )}
          <button
            onClick={handleClear}
            className="text-xs text-[var(--text-secondary)] hover:text-red-500 transition-colors"
          >
            {t('chat.clear')}
          </button>
        </div>
      ),
    });
    return () => clearSlots();
  }, [sessionName, currentSession, totalTokens, handleClear, navigate, t, setSlots, clearSlots]);

  if (!id) {
    return (
      <div className="flex items-center justify-center text-[var(--text-secondary)] text-sm" style={{ position: 'absolute', inset: 0 }}>
        {t('chat.selectSession')}
      </div>
    );
  }

  return (
    <div className="flex flex-col" style={{ position: 'absolute', inset: 0, overflow: 'hidden' }}>
      {/* 错误提示条 */}
      {error && (
        <div className="mx-4 mt-2 px-4 py-2.5 bg-red-50 dark:bg-red-900/10 border border-red-200 dark:border-red-800 rounded-xl text-sm text-red-600 dark:text-red-400 flex items-center justify-between">
          <span>{error}</span>
          <button onClick={clearError} className="text-red-400 hover:text-red-600 dark:hover:text-red-300 ml-2">
            <X className="w-4 h-4" />
          </button>
        </div>
      )}

      {/* 分屏布局：聊天区 + 右侧工作区 */}
      {/* 宽屏（md+）：工作区承载 Todos + Canvas stack；窄屏：Todos 贴近输入区，Canvas 全屏覆盖 */}
      <div style={{ display: 'flex', flex: '1 1 0%', minHeight: 0, position: 'relative' }}>
        {/* 聊天区：窄屏 Canvas 打开时隐藏；宽屏有工作区时占 50% */}
        <div
          className={`${canvasOpen ? 'hidden md:flex' : 'flex'} ${workspaceOpen ? (canvasOpen ? 'md:w-1/2' : 'md:flex-1') : 'md:w-full'} w-full transition-[width] duration-200`}
          style={{ flexDirection: 'column', minWidth: 0, minHeight: 0, overflow: 'hidden' }}
        >
          <MessageList
            key={id}
            messages={messages}
            loading={sending}
            streamingStatus={streaming ? agentStatus : null}
            onRegenerate={handleRegenerate}
          />
          <TodosList variant="mobile" />
          <ChatInput onSend={handleSend} onStop={handleStop} disabled={sending || streaming} />
        </div>
        {/* 右侧工作区：宽屏 stack，窄屏仅 Canvas 覆盖 */}
        {workspaceOpen && (
          <div className={`${canvasOpen ? 'absolute inset-0' : 'hidden'} md:relative md:inset-auto md:flex ${workspaceWidthClass} flex-col min-w-0 min-h-0 border-l border-[var(--border-color)] bg-[var(--bg-primary)]`}>
            <TodosList variant="desktop" />
            {canvasOpen && (
              <div className="flex min-h-0 flex-1 flex-col">
                <CanvasPanel />
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

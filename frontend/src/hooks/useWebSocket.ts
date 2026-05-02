import { useCallback, useRef } from 'react';
import type { WSMessage, InputRequest, Message } from '../types/api';
import { useHITLStore } from '../store/hitl';
import { useChatStore } from '../store/chat';
import { useTaskProgressStore, type TaskGroupEvent, type TaskProgressEvent, type AgentProgressEvent } from '../store/taskProgress';
import { useAgentActivityStore } from '../store/agentActivity';  // Sidebar SessionStatusDot 依赖 sessionStatus
import { useSessionStore } from '../store/session';
import { useTodosStore, type TodoSnapshot } from '../store/todos';
import { rfc3339Now } from '../utils/date';
import { maxConfirmedTimestamp } from '../store/chat';
import type { NodeClient } from '../api/node-client';
import { useWebSocketConnection } from './useWebSocketConnection';

interface MessagePayload {
  session_id?: string;
  role?: 'user' | 'assistant' | 'tool';
  content: string;
  reasoning_content?: string;
  partial?: boolean;
  timestamp?: string;
  tool_calls?: { id: string; name: string; arguments: string }[];
  usage?: { input_tokens: number; output_tokens: number };
  llm_duration?: number;
  tool_call_id?: string;
  tool_name?: string;
  is_error?: boolean;
}

interface ToolCallPayload {
  tool_call_id: string;
  tool_name: string;
  status: 'start' | 'success' | 'error';
  duration?: number;
  error?: string;
  session_id?: string;
}

interface AgentStatusPayload {
  session_id?: string;
  status: 'thinking' | 'tool_calling' | 'warning' | 'completed' | 'paused' | 'error';
  error?: string;
  message?: string;
  warning?: string;
}

interface UseWebSocketOptions {
  url: string;
  sessionId?: string;
  enabled?: boolean;
  onMessage?: (msg: WSMessage) => void;
  client?: NodeClient;
}

export function useWebSocket({ url, sessionId, enabled = true, onMessage, client }: UseWebSocketOptions) {
  const addHITLRequest = useHITLStore((s) => s.addRequest);
  const addChatMessage = useChatStore((s) => s.addMessage);
  const setStreaming = useChatStore((s) => s.setStreaming);
  const setAgentStatus = useChatStore((s) => s.setAgentStatus);
  const updateLastAssistant = useChatStore((s) => s.updateLastAssistant);
  const ensureAssistantMessage = useChatStore((s) => s.ensureAssistantMessage);
  const setToolCallStatus = useChatStore((s) => s.setToolCallStatus);
  const replaceStreamingMessage = useChatStore((s) => s.replaceStreamingMessage);
  const confirmUserMessage = useChatStore((s) => s.confirmUserMessage);

  // RAF 批量合并 partial 更新
  const pendingPartial = useRef<{ content: string; reasoning?: string } | null>(null);
  const partialRafId = useRef(0);

  // 错误消息限流
  const lastErrorMsg = useRef('');
  const lastErrorTime = useRef(0);

  const onMessageRef = useRef(onMessage);
  onMessageRef.current = onMessage;

  const handleMessage = useCallback((msg: WSMessage) => {
    const isOtherSession = (sid?: string): boolean => {
      if (!sid) return false;
      const cur = useChatStore.getState().currentSessionId;
      return !!(cur && sid !== cur);
    };

    switch (msg.type) {
      case 'input_request': {
        const req = msg.payload as InputRequest;
        if (isOtherSession(req.session_id)) break;
        addHITLRequest(req);
        useChatStore.getState().addInlineApproval(req);
        break;
      }

      case 'message': {
        const payload = msg.payload as MessagePayload;
        const sid = payload.session_id;
        if (isOtherSession(sid)) break;

        if (payload.partial) {
          // 特殊路径：partial=true 但带 tool_calls 时，表示本轮 LLM 已决策完成调用工具。
          // 后端 partial=true 的真正目的是阻止飞书卡片提前显示"完成"（见 react_processor.go:586-594 注释），
          // 对 WebSocket 聊天流而言这其实是一条"中间定型消息"，必须把 tool_calls 落到消息上
          // 才能实时渲染工具调用 chip。否则只能等刷新走 loadMessages 从 DB 拉，UX 裂开。
          if (payload.tool_calls && payload.tool_calls.length > 0) {
            // 清理 pending partial RAF，避免先前累积的 content 被 raf 回调覆盖到已定型的消息
            if (partialRafId.current) {
              cancelAnimationFrame(partialRafId.current);
              partialRafId.current = 0;
            }
            pendingPartial.current = null;

            const streamId = useChatStore.getState().streamingMessageId;
            const committed: Message = {
              role: 'assistant',
              content: payload.content,
              reasoning_content: payload.reasoning_content,
              timestamp: payload.timestamp,
              tool_calls: payload.tool_calls,
              usage: payload.usage,
              llm_duration: payload.llm_duration,
            };
            if (streamId) {
              replaceStreamingMessage(committed, streamId);
            } else {
              addChatMessage(committed, sid);
            }
            // 工具执行期间仍处于 streaming 态，前端 loading 指示器应保持
            setStreaming(true);
            break;
          }

          ensureAssistantMessage();
          setStreaming(true);
          pendingPartial.current = { content: payload.content, reasoning: payload.reasoning_content };
          if (!partialRafId.current) {
            partialRafId.current = requestAnimationFrame(() => {
              if (pendingPartial.current) {
                updateLastAssistant(pendingPartial.current.content, pendingPartial.current.reasoning);
                pendingPartial.current = null;
              }
              partialRafId.current = 0;
            });
          }
        } else if (payload.role === 'user') {
          if (payload.timestamp) {
            confirmUserMessage(payload.timestamp);
          }
        } else {
          const wasStreaming = useChatStore.getState().streaming;
          const streamId = useChatStore.getState().streamingMessageId;
          const fullMsg: Message = {
            role: payload.role || 'assistant',
            content: payload.content,
            reasoning_content: payload.reasoning_content,
            timestamp: payload.timestamp,
            tool_calls: payload.tool_calls,
            usage: payload.usage,
            llm_duration: payload.llm_duration,
            tool_call_id: payload.tool_call_id,
            tool_name: payload.tool_name,
            is_error: payload.is_error,
          };
          if (wasStreaming && fullMsg.role === 'assistant' && streamId) {
            replaceStreamingMessage(fullMsg, streamId);
          } else {
            setStreaming(false);
            addChatMessage(fullMsg, sid);
          }
        }
        break;
      }

      case 'agent_status': {
        const payload = msg.payload as AgentStatusPayload;
        if (isOtherSession(payload.session_id)) break;

        setAgentStatus(payload.status);
        if (payload.status === 'completed' || payload.status === 'paused') {
          setStreaming(false);
          setAgentStatus(null);
        } else if (payload.status === 'error') {
          setStreaming(false);
          setAgentStatus(null);
          if (payload.error) {
            // 时间戳锚定在最后一条已确认消息之后，避免服务端时钟偏移导致乱序
            const msgs = useChatStore.getState().messages;
            const anchor = maxConfirmedTimestamp(msgs);
            const errorTs = anchor
              ? new Date(new Date(anchor).getTime() + 1).toISOString()
              : rfc3339Now();
            addChatMessage({
              role: 'assistant',
              content: payload.error,
              timestamp: errorTs,
              is_error: true,
            }, payload.session_id);
          }
        } else if (payload.status === 'thinking' || payload.status === 'tool_calling') {
          setStreaming(true);
        }
        useAgentActivityStore.getState().onAgentStatus(
          payload.session_id ?? '',
          payload.status
        );
        break;
      }

      case 'error': {
        const errorPayload = msg.payload as { message?: string; session_id?: string };
        if (isOtherSession(errorPayload.session_id)) break;
        const errMsg = errorPayload.message || '发生了一个错误';
        const now = Date.now();
        if (errMsg === lastErrorMsg.current && now - lastErrorTime.current < 3000) break;
        lastErrorMsg.current = errMsg;
        lastErrorTime.current = now;
        addChatMessage({
          role: 'assistant',
          content: errMsg,
          timestamp: rfc3339Now(),
          is_error: true,
        }, errorPayload.session_id);
        break;
      }

      case 'tool_call': {
        const tcPayload = msg.payload as ToolCallPayload;
        if (isOtherSession(tcPayload.session_id)) break;
        setToolCallStatus(tcPayload.tool_call_id, {
          id: tcPayload.tool_call_id,
          name: tcPayload.tool_name,
          status: tcPayload.status === 'start' ? 'running' : tcPayload.status === 'error' ? 'error' : 'success',
          duration: tcPayload.duration,
          error: tcPayload.error,
        });
        break;
      }

      case 'task_group':
        useTaskProgressStore.getState().setTaskGroup(msg.payload as TaskGroupEvent);
        break;
      case 'task_progress': {
        const tp = msg.payload as TaskProgressEvent;
        const groupId = tp.groupId || tp.group_id;
        const taskId = tp.taskId || tp.task_id;
        if (groupId && taskId) {
          useTaskProgressStore.getState().updateTask(groupId, taskId, tp);
        }
        break;
      }
      case 'agent_progress':
        useTaskProgressStore.getState().updateAgentProgress(msg.payload as AgentProgressEvent);
        break;

      case 'todo_snapshot': {
        const snapshot = msg.payload as TodoSnapshot;
        if (isOtherSession(snapshot.session_id)) break;
        useTodosStore.getState().applySnapshot(snapshot);
        break;
      }

      case 'session_title': {
        const payload = msg.payload as { session_id: string; title: string };
        useSessionStore.getState().updateSessionName(payload.session_id, payload.title);
        break;
      }

      default:
        break;
    }

    onMessageRef.current?.(msg);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleConnected = useCallback(() => {
    const sid = useChatStore.getState().currentSessionId;
    if (sid && client) {
      client.getPendingInput(sid).then((pending) => {
        const requests = pending as InputRequest[];
        for (const req of requests) {
          addHITLRequest(req);
          useChatStore.getState().addInlineApproval(req);
        }
      }).catch(() => {});
      useTodosStore.getState().loadSnapshot(client, sid).catch(() => {});
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client]);

  const handleDisconnected = useCallback(() => {
    // 清理 RAF 定时器
    if (partialRafId.current) {
      cancelAnimationFrame(partialRafId.current);
      partialRafId.current = 0;
    }
    pendingPartial.current = null;
    setStreaming(false);
    setAgentStatus(null);
    // 注意：这里刻意不再清 currentSessionId。
    // 历史实现会 setCurrentSessionId(null)，但 WS 是会话作用域重连的
    // （AppShell sessionId 变化 → 关旧→开新），onclose 会被正常触发，
    // 若此时把 store 里的 currentSessionId 清零：
    // (a) 新连接拿到的首批 partial chunk 到达时，handleMessage 里
    //     addChatMessage(msg, sid) 会命中 chat.ts addMessage 的
    //     `sessionId && currentSessionId && sessionId !== currentSessionId`
    //     判断边界——虽然 currentSessionId=null 当前不 drop，但任何
    //     未来的防御性收紧都会误伤；
    // (b) 下游 handleConnected 不会把 currentSessionId 再填回去，
    //     依赖 Chat.tsx loadMessages 异步设置——和 WS 重连、首帧到达
    //     形成三方 race，im-streaming-reply Sprint 12 之后前端
    //     100% 吞掉 AI 回复的根因之一。
    // URL 仍在 /sessions/:id，chat store 的会话 id 不应被 WS 生命周期
    // 单方面清零——真正的 source of truth 是 loadMessages / sendMessage。
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const { connected, send } = useWebSocketConnection({
    url,
    sessionId,
    enabled,
    onMessage: handleMessage,
    onConnected: handleConnected,
    onDisconnected: handleDisconnected,
  });

  return { connected, send };
}

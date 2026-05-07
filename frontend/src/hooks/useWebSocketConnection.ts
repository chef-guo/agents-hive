import { useEffect, useRef, useCallback, useState } from 'react';
import type { WSMessage } from '../types/api';
import { ensureFreshToken } from '../store/auth';

export interface UseWebSocketConnectionOptions {
  url: string;
  sessionId?: string;
  enabled?: boolean;
  onMessage?: (msg: WSMessage) => void;
  onConnected?: () => void;
  onDisconnected?: () => void;
}

const MAX_RETRIES = 10;

/**
 * 纯 WebSocket 连接管理 hook（连接/重连/心跳/auth）。
 * 不含任何业务逻辑（message/tool_call/agent_status 处理）。
 */
export function useWebSocketConnection({
  url,
  sessionId,
  enabled = true,
  onMessage,
  onConnected,
  onDisconnected,
}: UseWebSocketConnectionOptions) {
  const wsRef = useRef<WebSocket | null>(null);
  const reconnectTimer = useRef<ReturnType<typeof setTimeout>>(undefined);
  const retryCount = useRef(0);
  const [connected, setConnected] = useState(false);
  const mountedRef = useRef(true);
  const connectRef = useRef<() => void>(() => {});
  const connectingRef = useRef(false);
	const connectSeqRef = useRef(0);
	const connectWithTokenRef = useRef<(token: string | null, connectSeq: number) => void>(() => {});

  // 用 ref 保存最新的回调，避免 connect 的 useCallback 依赖频繁变化
  const onMessageRef = useRef(onMessage);
  const onConnectedRef = useRef(onConnected);
  const onDisconnectedRef = useRef(onDisconnected);

  useEffect(() => {
    onMessageRef.current = onMessage;
    onConnectedRef.current = onConnected;
    onDisconnectedRef.current = onDisconnected;
  }, [onMessage, onConnected, onDisconnected]);

  const connectWithToken = useCallback((token: string | null, connectSeq: number) => {
    if (!mountedRef.current || !enabled || !url || connectSeq !== connectSeqRef.current) return;
    if (wsRef.current && (wsRef.current.readyState === WebSocket.CONNECTING || wsRef.current.readyState === WebSocket.OPEN)) {
      return;
    }
    const protocols = token ? [`bearer-${token}`, 'v1'] : undefined;
    const wsUrl = sessionId ? `${url}${url.includes('?') ? '&' : '?'}session_id=${encodeURIComponent(sessionId)}` : url;
    const ws = new WebSocket(wsUrl, protocols);
    connectingRef.current = false;
    wsRef.current = ws;

    ws.onopen = () => {
      if (mountedRef.current) {
        setConnected(true);
        retryCount.current = 0;
        onConnectedRef.current?.();
      }
    };

    ws.onmessage = (event) => {
      try {
        const msg: WSMessage = JSON.parse(event.data);

        // 自动响应 ping
        if (msg.type === 'ping' && ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'pong', payload: {} }));
          return;
        }

        onMessageRef.current?.(msg);
      } catch (err) {
        console.error('[WebSocket] 消息解析失败:', err);
      }
    };

    ws.onclose = (event) => {
      if (wsRef.current !== ws) return;

      setConnected(false);
      wsRef.current = null;
      onDisconnectedRef.current?.();

      // auth 失败码（4401）→ 强制刷新后直接用新 token 重连，避免重复走一次 freshness 检查。
      if (event.code === 4401 && mountedRef.current) {
        const retrySeq = ++connectSeqRef.current;
        connectingRef.current = true;
        ensureFreshToken({ force: true }).then((newToken) => {
          if (newToken && mountedRef.current) {
						connectWithTokenRef.current(newToken, retrySeq);
          } else {
            window.location.href = '/login';
          }
        }).catch(() => {
          window.location.href = '/login';
        }).finally(() => {
          connectingRef.current = false;
        });
        return;
      }

      // 指数退避重连
      if (enabled && mountedRef.current) {
        if (retryCount.current >= MAX_RETRIES) {
          console.warn(`[WebSocket] 已达最大重连次数 (${MAX_RETRIES})，停止重连`);
          return;
        }
        const delay = Math.min(3000 * Math.pow(2, retryCount.current), 30000);
        retryCount.current++;
        clearTimeout(reconnectTimer.current);
        reconnectTimer.current = setTimeout(() => {
          if (mountedRef.current) connectRef.current();
        }, delay);
      }
    };

    ws.onerror = () => {
      ws.close();
    };
	  }, [enabled, sessionId, url]);

	useEffect(() => {
		connectWithTokenRef.current = connectWithToken;
	}, [connectWithToken]);

  const connect = useCallback(() => {
    if (!enabled || !url || !mountedRef.current) return;

    if (wsRef.current && (wsRef.current.readyState === WebSocket.CONNECTING || wsRef.current.readyState === WebSocket.OPEN)) {
      return;
    }
    if (connectingRef.current) return;

    connectingRef.current = true;
    const connectSeq = ++connectSeqRef.current;
    ensureFreshToken().then((token) => {
      connectWithToken(token, connectSeq);
    }).catch(() => {
      if (mountedRef.current) {
        if (retryCount.current >= MAX_RETRIES) return;
        const delay = Math.min(3000 * Math.pow(2, retryCount.current), 30000);
        retryCount.current++;
        clearTimeout(reconnectTimer.current);
        reconnectTimer.current = setTimeout(() => {
          if (mountedRef.current) connectRef.current();
        }, delay);
      }
    }).finally(() => {
      connectingRef.current = false;
    });
  }, [connectWithToken, enabled, url]);

  useEffect(() => {
    mountedRef.current = true;
    connectRef.current = connect;
    connect();
    return () => {
      mountedRef.current = false;
			const nextConnectSeq = connectSeqRef.current + 1;
			connectSeqRef.current = nextConnectSeq;
      clearTimeout(reconnectTimer.current);
      if (wsRef.current) {
        wsRef.current.onclose = null;
        wsRef.current.close();
        wsRef.current = null;
      }
    };
  }, [connect]);

  const send = useCallback((msg: WSMessage) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify(msg));
    }
  }, []);

  return { connected, send };
}

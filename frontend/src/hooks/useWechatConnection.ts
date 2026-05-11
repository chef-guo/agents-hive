import { useCallback, useEffect, useState } from 'react';
import {
  getWeChatStatus,
  listWeChatConversations,
  loginWeChat,
  logoutWeChat,
  reloginWeChat,
  streamWeChatEvents,
  type WeChatConnectionStatus,
  type WeChatConversation,
  type WeChatEvent,
} from '../api/wechat';

export function useWechatConnection() {
  const [status, setStatus] = useState<WeChatConnectionStatus | null>(null);
  const [conversations, setConversations] = useState<WeChatConversation[]>([]);
  const [qrUrl, setQrUrl] = useState('');
  const [lastEvent, setLastEvent] = useState<WeChatEvent | null>(null);
  const [loading, setLoading] = useState(true);
  const [actionLoading, setActionLoading] = useState<'login' | 'relogin' | 'logout' | null>(null);
  const [streamConnected, setStreamConnected] = useState(false);
  const [error, setError] = useState('');

  const refresh = useCallback(async () => {
    setError('');
    const [nextStatus, nextConversations] = await Promise.all([
      getWeChatStatus(),
      listWeChatConversations().catch(() => []),
    ]);
    setStatus(nextStatus);
    setConversations(nextConversations);
    if (nextStatus.status !== 'waiting_qr_scan') {
      setQrUrl('');
    }
  }, []);

  useEffect(() => {
    let mounted = true;
    setLoading(true);
    refresh()
      .catch((err) => {
        if (!mounted) return;
        setError(err instanceof Error ? err.message : String(err));
      })
      .finally(() => {
        if (mounted) setLoading(false);
      });
    return () => {
      mounted = false;
    };
  }, [refresh]);

  useEffect(() => {
    const controller = new AbortController();
    let stopped = false;

    streamWeChatEvents((event) => {
      setStreamConnected(true);
      setLastEvent(event);
      if (event.qr_url) setQrUrl(event.qr_url);
      if (event.status) {
        setStatus((current) => current ? { ...current, status: event.status ?? current.status, error: event.error ?? '' } : current);
      }
      if (event.error) setError(event.error);
      if (event.type === 'status' && event.status === 'online') {
        refresh().catch(() => undefined);
      }
    }, controller.signal)
      .catch((err) => {
        if (stopped || controller.signal.aborted) return;
        setStreamConnected(false);
        setError(err instanceof Error ? err.message : String(err));
      });

    return () => {
      stopped = true;
      controller.abort();
      setStreamConnected(false);
    };
  }, [refresh]);

  const login = useCallback(async () => {
    setActionLoading('login');
    setError('');
    try {
      const next = await loginWeChat();
      setStatus(next);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setActionLoading(null);
    }
  }, [refresh]);

  const relogin = useCallback(async () => {
    setActionLoading('relogin');
    setQrUrl('');
    setError('');
    try {
      const next = await reloginWeChat();
      setStatus(next);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setActionLoading(null);
    }
  }, [refresh]);

  const logout = useCallback(async () => {
    setActionLoading('logout');
    setError('');
    try {
      await logoutWeChat();
      setQrUrl('');
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setActionLoading(null);
    }
  }, [refresh]);

  return {
    status,
    conversations,
    qrUrl,
    lastEvent,
    loading,
    actionLoading,
    streamConnected,
    error,
    refresh,
    login,
    relogin,
    logout,
  };
}

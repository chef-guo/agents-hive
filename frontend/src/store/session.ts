import { create } from 'zustand';
import type { Session, SessionDetail } from '../types/api';
import type { NodeClient } from '../api/node-client';
import { useToastStore } from './toast';

interface SessionState {
  sessions: Session[];
  currentSession: SessionDetail | null;
  loading: boolean;
  error: string | null;
  // 操作
  fetchSessions: (client: NodeClient) => Promise<void>;
  fetchSession: (client: NodeClient, id: string) => Promise<void>;
  createSession: (client: NodeClient, name: string) => Promise<string>;
  deleteSession: (client: NodeClient, id: string) => Promise<void>;
  clearSession: (client: NodeClient, id: string) => Promise<void>;
  setCurrentSession: (session: SessionDetail | null) => void;
  updateSessionName: (id: string, name: string) => void;
  // 收藏 & 标签
  starSession: (client: NodeClient, id: string, starred: boolean) => Promise<void>;
  updateSessionTags: (client: NodeClient, id: string, tags: string[]) => Promise<void>;
  clearError: () => void;
}

export const useSessionStore = create<SessionState>((set) => ({
  sessions: [],
  currentSession: null,
  loading: false,
  error: null,

  fetchSessions: async (client) => {
    set({ loading: true, error: null });
    try {
      const sessions = await client.listSessions();
      set({ sessions, loading: false });
    } catch (e: unknown) {
      const errorMsg = e instanceof Error ? e.message : '获取会话列表失败';
      set({ error: errorMsg, loading: false });
    }
  },

  fetchSession: async (client, id) => {
    set({ loading: true, error: null });
    try {
      const session = await client.getSession(id);
      set({ currentSession: session, loading: false });
    } catch (e: unknown) {
      const errorMsg = e instanceof Error ? e.message : '获取会话列表失败';
      set({ currentSession: null, error: errorMsg, loading: false });
    }
  },

  createSession: async (client, name) => {
    set({ error: null });
    try {
      const res = await client.createSession(name);
      // 刷新列表
      const sessions = await client.listSessions();
      set({ sessions });
      return res.session_id;
    } catch (e: unknown) {
      const errorMsg = e instanceof Error ? e.message : '创建会话失败';
      set({ error: errorMsg });
      throw e;
    }
  },

  deleteSession: async (client, id) => {
    try {
      await client.deleteSession(id);
      set((s) => ({
        sessions: s.sessions.filter((sess) => sess.id !== id),
        currentSession: s.currentSession?.id === id ? null : s.currentSession,
      }));
    } catch (e: unknown) {
      const errorMsg = e instanceof Error ? e.message : '删除会话失败';
      set({ error: errorMsg });
    }
  },

  clearSession: async (client, id) => {
    try {
      await client.clearSession(id);
    } catch (e: unknown) {
      const errorMsg = e instanceof Error ? e.message : '清空会话失败';
      set({ error: errorMsg });
    }
  },

  starSession: async (client, id, starred) => {
    // 乐观更新
    set((s) => ({
      sessions: s.sessions.map((sess) =>
        sess.id === id ? { ...sess, is_starred: starred } : sess
      ),
    }));
    try {
      await client.starSession(id, starred);
    } catch (e: unknown) {
      // 回滚
      set((s) => ({
        sessions: s.sessions.map((sess) =>
          sess.id === id ? { ...sess, is_starred: !starred } : sess
        ),
        error: e instanceof Error ? e.message : '收藏操作失败',
      }));
      useToastStore.getState().addToast('error', '收藏失败');
    }
  },

  updateSessionTags: async (client, id, tags) => {
    // 乐观更新
    set((s) => ({
      sessions: s.sessions.map((sess) =>
        sess.id === id ? { ...sess, tags } : sess
      ),
    }));
    try {
      await client.updateSessionTags(id, tags);
    } catch (e: unknown) {
      set({ error: e instanceof Error ? e.message : '更新标签失败' });
      useToastStore.getState().addToast('error', '更新标签失败');
      throw e;
    }
  },

  setCurrentSession: (session) => set({ currentSession: session }),
  updateSessionName: (id, name) =>
    set((s) => ({
      sessions: s.sessions.map((sess) =>
        sess.id === id ? { ...sess, name, message_count: Math.max(sess.message_count, 1) } : sess
      ),
      currentSession:
        s.currentSession?.id === id
          ? { ...s.currentSession, name }
          : s.currentSession,
    })),
  clearError: () => set({ error: null }),
}));

import { create } from 'zustand';
import type { NodeClient } from '../api/node-client';

export type TodoStatus = 'pending' | 'in_progress' | 'completed' | 'cancelled';
export type PlanStatus = 'none' | 'planning' | 'awaiting_approval' | 'executing' | 'paused' | 'completed' | 'failed';

export interface Todo {
  id: string;
  session_id: string;
  content: string;
  status: TodoStatus;
  source?: string;
  source_change_id?: string;
  source_revision?: number;
  order: number;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface TodoSnapshot {
  session_id: string;
  plan_status: PlanStatus;
  plan_version: number;
  todos: Todo[];
  source?: string;
  trace_id?: string;
  span_id?: string;
  source_tool_call_id?: string;
  source_change_id?: string;
  source_revision?: number;
  updated_at: string;
}

export function shouldShowTodosPanel(snapshot: { plan_status: PlanStatus; todos: unknown[] } | null): boolean {
  return !!snapshot && (snapshot.todos.length > 0 || snapshot.plan_status !== 'none');
}

interface TodosState {
  currentSessionId: string | null;
  snapshot: TodoSnapshot | null;
  localVersion: number;
  loading: boolean;
  error: string | null;
  loadSnapshot: (client: NodeClient, sessionId: string) => Promise<void>;
  applySnapshot: (snapshot: TodoSnapshot) => void;
  clear: () => void;
}

function isRecoverableTodosAPIError(error: unknown): boolean {
  if (!(error instanceof Error) || !('code' in error)) return false;
  const code = (error as { code?: number }).code;
  return code === 404 || code === 503 || code === 1006 || code === 1007;
}

export const useTodosStore = create<TodosState>((set) => ({
  currentSessionId: null,
  snapshot: null,
  localVersion: 0,
  loading: false,
  error: null,

  loadSnapshot: async (client, sessionId) => {
    set((state) => ({
      currentSessionId: sessionId,
      loading: true,
      error: null,
      ...(state.currentSessionId !== sessionId ? { snapshot: null, localVersion: 0 } : {}),
    }));
    try {
      const snapshot = await client.getTodoSnapshot(sessionId);
      set((state) => {
        if (state.currentSessionId !== sessionId || snapshot.session_id !== sessionId) {
          return state;
        }
        if (snapshot.plan_version <= state.localVersion) {
          return { loading: false };
        }
        return {
          snapshot,
          localVersion: snapshot.plan_version,
          loading: false,
          error: null,
        };
      });
    } catch (error: unknown) {
      if (isRecoverableTodosAPIError(error)) {
        set((state) => state.currentSessionId === sessionId
          ? { snapshot: null, localVersion: 0, loading: false, error: null }
          : state);
        return;
      }
      set((state) => state.currentSessionId === sessionId
        ? {
            loading: false,
            error: error instanceof Error ? error.message : 'Failed to load todos',
          }
        : state);
    }
  },

  applySnapshot: (snapshot) => set((state) => {
    if (state.currentSessionId && snapshot.session_id !== state.currentSessionId) return state;
    if (snapshot.plan_version <= state.localVersion) return state;
    return {
      currentSessionId: snapshot.session_id,
      snapshot,
      localVersion: snapshot.plan_version,
      error: null,
    };
  }),

  clear: () => set({ currentSessionId: null, snapshot: null, localVersion: 0, loading: false, error: null }),
}));

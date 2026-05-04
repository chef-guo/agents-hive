import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useTodosStore, type TodoSnapshot } from '../todos';
import type { NodeClient } from '../../api/node-client';

function pausedSnapshot(overrides: Partial<TodoSnapshot> = {}): TodoSnapshot {
  return {
    session_id: 'sess-1',
    plan_status: 'paused',
    plan_version: 7,
    runtime_epoch: 'epoch-1',
    todos: [
      {
        id: 'next',
        session_id: 'sess-1',
        content: '继续实现',
        status: 'pending',
        order: 0,
        version: 7,
        created_at: '2026-05-02T00:00:00Z',
        updated_at: '2026-05-02T00:00:00Z',
      },
    ],
    updated_at: '2026-05-02T00:00:00Z',
    ...overrides,
  };
}

function nodeClientMock(resumeTodos: NodeClient['resumeTodos']): NodeClient {
  return {
    resumeTodos,
  } as NodeClient;
}

beforeEach(() => {
  useTodosStore.setState({
    currentSessionId: null,
    snapshot: null,
    localVersion: 0,
    loading: false,
    resuming: false,
    error: null,
  });
});

describe('useTodosStore.resumePlan', () => {
  it('executes guarded resume and applies returned snapshot', async () => {
    const snapshot = pausedSnapshot();
    const resumed = pausedSnapshot({ plan_status: 'executing', plan_version: 8, runtime_epoch: 'epoch-2' });
    const resumeTodos = vi.fn<NodeClient['resumeTodos']>().mockResolvedValue({
      action: { allowed: true, mode: 'manual' },
      snapshot: resumed,
    });
    useTodosStore.setState({ currentSessionId: 'sess-1', snapshot, localVersion: 7 });

    await useTodosStore.getState().resumePlan(nodeClientMock(resumeTodos));

    expect(resumeTodos).toHaveBeenCalledWith('sess-1', 7, 'epoch-1');
    expect(useTodosStore.getState().snapshot?.plan_status).toBe('executing');
    expect(useTodosStore.getState().localVersion).toBe(8);
    expect(useTodosStore.getState().resuming).toBe(false);
  });

  it('blocks execute resume when runtime epoch is missing', async () => {
    const resumeTodos = vi.fn<NodeClient['resumeTodos']>();
    useTodosStore.setState({
      currentSessionId: 'sess-1',
      snapshot: pausedSnapshot({ runtime_epoch: undefined }),
      localVersion: 7,
    });

    await useTodosStore.getState().resumePlan(nodeClientMock(resumeTodos));

    expect(resumeTodos).not.toHaveBeenCalled();
    expect(useTodosStore.getState().error).toContain('runtime epoch');
  });
});

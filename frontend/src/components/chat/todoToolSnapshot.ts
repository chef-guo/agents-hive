import type { PlanStatus, TodoStatus } from '../../store/todos';

export interface InlineTodo {
  id: string;
  content: string;
  status: TodoStatus;
  order: number;
}

export interface InlineTodoSnapshot {
  plan_status: PlanStatus;
  plan_version?: number;
  todos: InlineTodo[];
}

const TODO_STATUSES = new Set<TodoStatus>(['pending', 'in_progress', 'completed', 'cancelled']);
const PLAN_STATUSES = new Set<PlanStatus>([
  'none',
  'planning',
  'awaiting_approval',
  'executing',
  'paused',
  'completed',
  'failed',
]);

export function isTodoWriteTool(name: string): boolean {
  const normalized = name.toLowerCase().replace(/[-\s]/g, '_');
  return normalized === 'todo_write' || normalized === 'todowrite';
}

export function parseTodoToolSnapshot(result?: string): InlineTodoSnapshot | null {
  if (!result) return null;

  let parsed: unknown;
  try {
    parsed = JSON.parse(result);
  } catch {
    return null;
  }

  if (!parsed || typeof parsed !== 'object') return null;
  const value = parsed as Record<string, unknown>;
  if (!Array.isArray(value.todos)) return null;
  if (typeof value.plan_status !== 'string' || !PLAN_STATUSES.has(value.plan_status as PlanStatus)) {
    return null;
  }

  const todos = value.todos
    .map((raw, index): InlineTodo | null => {
      if (!raw || typeof raw !== 'object') return null;
      const item = raw as Record<string, unknown>;
      if (typeof item.id !== 'string' || typeof item.content !== 'string') return null;
      const status = typeof item.status === 'string' && TODO_STATUSES.has(item.status as TodoStatus)
        ? item.status as TodoStatus
        : 'pending';
      const order = typeof item.order === 'number' ? item.order : index;
      return { id: item.id, content: item.content, status, order };
    })
    .filter((todo): todo is InlineTodo => todo !== null)
    .sort((a, b) => a.order - b.order);

  return {
    plan_status: value.plan_status as PlanStatus,
    plan_version: typeof value.plan_version === 'number' ? value.plan_version : undefined,
    todos,
  };
}

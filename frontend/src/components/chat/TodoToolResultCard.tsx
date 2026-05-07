import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { CheckCircle2, Circle, ClipboardList, Clock3, MinusCircle } from 'lucide-react';
import type { TodoStatus } from '../../store/todos';
import type { InlineTodo } from './todoToolSnapshot';
import { parseTodoToolSnapshot } from './todoToolSnapshot';

interface TodoToolResultCardProps {
  result?: string;
}

export function TodoToolResultCard({ result }: TodoToolResultCardProps) {
  const { t } = useTranslation();
  const snapshot = useMemo(() => parseTodoToolSnapshot(result), [result]);

  if (!snapshot) return null;

  return (
    <section
      data-testid="inline-todos-list"
      className="not-prose mb-3 max-w-3xl"
      aria-label={t('todos.ariaLabel')}
    >
      <div className="mb-1.5 flex items-center gap-2 px-1">
        <ClipboardList className="h-4 w-4 shrink-0 text-[var(--accent-600)] dark:text-[var(--accent-300)]" aria-hidden="true" />
        <div className="min-w-0 flex-1">
          <div className="text-sm font-semibold leading-5 text-[var(--text-primary)]">
            {t('todos.inlineUpdated')}
          </div>
          <div className="text-[11px] leading-4 text-[var(--text-secondary)]">
            {t(`todos.planStatus.${snapshot.plan_status}`)}
            {snapshot.plan_version !== undefined ? ` · v${snapshot.plan_version}` : ''}
          </div>
        </div>
        <span className="shrink-0 text-[11px] font-medium text-[var(--text-secondary)]">
          {t('todos.taskCount', { count: snapshot.todos.length })}
        </span>
      </div>

      {snapshot.todos.length > 0 ? (
        <ol className="space-y-0.5">
          {snapshot.todos.map((todo) => (
            <InlineTodoItem key={todo.id} todo={todo} />
          ))}
        </ol>
      ) : (
        <p className="px-1 py-1.5 text-xs text-[var(--text-secondary)]">
          {t('todos.emptyActivePlan')}
        </p>
      )}
    </section>
  );
}

function InlineTodoItem({ todo }: { todo: InlineTodo }) {
  const { t } = useTranslation();
  const statusLabel = t(`todos.status.${todo.status}`);
  const isMuted = todo.status === 'completed' || todo.status === 'cancelled';

  return (
    <li className="grid grid-cols-[18px_minmax(0,1fr)_auto] items-start gap-2.5 rounded-md px-1 py-1.5 text-sm hover:bg-[var(--bg-secondary)]">
      <span className={`mt-0.5 ${statusTextColor(todo.status)}`}>
        <TodoStatusIcon status={todo.status} label={statusLabel} />
      </span>
      <span className={`${isMuted ? 'text-[var(--text-secondary)]' : 'text-[var(--text-primary)]'} break-words leading-5`}>
        {todo.content}
      </span>
      <span className="whitespace-nowrap pt-0.5 text-[11px] font-medium text-[var(--text-secondary)]">
        {statusLabel}
      </span>
    </li>
  );
}

function TodoStatusIcon({ status, label }: { status: TodoStatus; label: string }) {
  switch (status) {
    case 'in_progress':
      return <Clock3 className="h-4 w-4" aria-label={label} />;
    case 'completed':
      return <CheckCircle2 className="h-4 w-4" aria-label={label} />;
    case 'cancelled':
      return <MinusCircle className="h-4 w-4" aria-label={label} />;
    case 'pending':
    default:
      return <Circle className="h-4 w-4" aria-label={label} />;
  }
}

function statusTextColor(status: TodoStatus): string {
  switch (status) {
    case 'in_progress':
      return 'text-[var(--accent-600)] dark:text-[var(--accent-300)]';
    case 'completed':
      return 'text-[var(--success)]';
    case 'cancelled':
      return 'text-[var(--text-secondary)]';
    case 'pending':
    default:
      return 'text-[var(--text-secondary)]';
  }
}

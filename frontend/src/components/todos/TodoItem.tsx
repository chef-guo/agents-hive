import { useTranslation } from 'react-i18next';
import { CheckCircle2, Circle, Clock3, MinusCircle } from 'lucide-react';
import type { Todo, TodoStatus } from '../../store/todos';

const statusStyles: Record<TodoStatus, string> = {
  pending: 'text-[var(--text-secondary)]',
  in_progress: 'text-[var(--accent-600)] dark:text-[var(--accent-300)]',
  completed: 'text-[var(--success)]',
  cancelled: 'text-[var(--text-secondary)]',
};

function TodoStatusIcon({ status, label }: { status: TodoStatus; label: string }) {
  switch (status) {
    case 'in_progress':
      return <Clock3 className="w-4 h-4" aria-label={label} />;
    case 'completed':
      return <CheckCircle2 className="w-4 h-4" aria-label={label} />;
    case 'cancelled':
      return <MinusCircle className="w-4 h-4" aria-label={label} />;
    case 'pending':
    default:
      return <Circle className="w-4 h-4" aria-label={label} />;
  }
}

export function TodoItem({
  todo,
  source,
  sourceChangeId,
  sourceRevision,
}: {
  todo: Todo;
  source?: string;
  sourceChangeId?: string;
  sourceRevision?: number;
}) {
  const { t } = useTranslation();
  const isMuted = todo.status === 'completed' || todo.status === 'cancelled';
  const effectiveSource = todo.source || source;
  const hasProjectedSource = !!effectiveSource && effectiveSource === 'spec_projected';
  const statusLabel = t(`todos.status.${todo.status}`);
  const sourceTitle = hasProjectedSource
    ? t('todos.source.specProjectedDetails', {
        changeId: todo.source_change_id || sourceChangeId || '-',
        revision: todo.source_revision || sourceRevision || '-',
      })
    : undefined;

  return (
    <li
      className={`grid grid-cols-[18px_minmax(0,1fr)_auto] items-start gap-2.5 rounded-md py-1.5 pr-1 text-sm ${
        hasProjectedSource
          ? 'border-l-4 border-[var(--accent-border)] pl-2 bg-[var(--accent-subtle)]'
          : 'pl-0'
      }`}
      title={sourceTitle}
    >
      <span className={`mt-0.5 ${statusStyles[todo.status]}`}>
        <TodoStatusIcon status={todo.status} label={statusLabel} />
      </span>
      <span className={`${isMuted ? 'text-[var(--text-secondary)]' : 'text-[var(--text-primary)]'} break-words`}>
        {todo.content}
      </span>
      <span className="text-[10px] uppercase tracking-normal text-[var(--text-secondary)] whitespace-nowrap">
        {statusLabel}
      </span>
    </li>
  );
}

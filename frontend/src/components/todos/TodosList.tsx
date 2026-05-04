import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ChevronDown, ClipboardList, Play } from 'lucide-react';
import { useNodeClient } from '../../hooks/useNodeClient';
import { shouldShowTodosPanel, useTodosStore, type PlanStatus } from '../../store/todos';
import { TodoItem } from './TodoItem';

type TodosListVariant = 'desktop' | 'mobile';

export function TodosList({ variant = 'desktop' }: { variant?: TodosListVariant }) {
  const { t } = useTranslation();
  const client = useNodeClient();
  const snapshot = useTodosStore((s) => s.snapshot);
  const resuming = useTodosStore((s) => s.resuming);
  const error = useTodosStore((s) => s.error);
  const resumePlan = useTodosStore((s) => s.resumePlan);
  const [mobileOpen, setMobileOpen] = useState(false);

  const orderedTodos = useMemo(
    () => (snapshot ? [...snapshot.todos].sort((a, b) => a.order - b.order) : []),
    [snapshot],
  );

  if (!snapshot || !shouldShowTodosPanel(snapshot)) return null;
  const visibleSnapshot = snapshot;

  if (variant === 'mobile') {
    return (
      <div className="md:hidden shrink-0 border-t border-[var(--border-color)] bg-[var(--bg-primary)] px-3 py-2">
        <section
          className="relative rounded-lg border border-[var(--border-color)] bg-[var(--bg-card)] shadow-sm"
          role="complementary"
          aria-label={t('todos.ariaLabel')}
        >
          <button
            type="button"
            onClick={() => setMobileOpen((open) => !open)}
            className="flex h-10 w-full items-center gap-2 px-3 text-left"
            aria-expanded={mobileOpen}
          >
            <ClipboardList className="h-4 w-4 shrink-0 text-[var(--accent-600)] dark:text-[var(--accent-300)]" />
            <span className="min-w-0 flex-1 truncate text-sm font-semibold text-[var(--text-primary)]">
              {t('todos.title')}
            </span>
            <span className="text-xs text-[var(--text-secondary)]">
              {orderedTodos.length}
            </span>
            <ChevronDown className={`h-4 w-4 shrink-0 text-[var(--text-secondary)] transition-transform ${mobileOpen ? 'rotate-180' : ''}`} />
          </button>
          {mobileOpen && (
            <div className="absolute inset-x-0 bottom-12 z-20 max-h-[80vh] overflow-hidden rounded-lg border border-[var(--border-color)] bg-[var(--bg-card)] shadow-xl">
              <TodosPanelBody
                orderedTodos={orderedTodos}
                planStatus={visibleSnapshot.plan_status}
                source={visibleSnapshot.source}
                sourceChangeId={visibleSnapshot.source_change_id}
                sourceRevision={visibleSnapshot.source_revision}
                resuming={resuming}
                error={error}
                onResume={() => resumePlan(client)}
                className="max-h-[calc(80vh-3rem)]"
              />
            </div>
          )}
        </section>
      </div>
    );
  }

  return (
    <section
      className="todos-panel hidden md:flex min-h-0 w-80 shrink-0 flex-col border-b border-[var(--border-color)] bg-[var(--bg-card)]"
      role="complementary"
      aria-label={t('todos.ariaLabel')}
    >
      <div className="flex h-10 shrink-0 items-center gap-2 border-b border-[var(--border-color)] px-3">
        <ClipboardList className="h-4 w-4 text-[var(--accent-600)] dark:text-[var(--accent-300)]" />
        <h2 className="min-w-0 flex-1 truncate text-sm font-semibold text-[var(--text-primary)]">
          {t('todos.title')}
        </h2>
        <span className="text-xs text-[var(--text-secondary)]">
          {t(`todos.planStatus.${visibleSnapshot.plan_status}`)}
        </span>
      </div>
      <TodosPanelBody
        orderedTodos={orderedTodos}
        planStatus={visibleSnapshot.plan_status}
        source={visibleSnapshot.source}
        sourceChangeId={visibleSnapshot.source_change_id}
        sourceRevision={visibleSnapshot.source_revision}
        resuming={resuming}
        error={error}
        onResume={() => resumePlan(client)}
        className="max-h-72"
      />
    </section>
  );
}

function TodosPanelBody({
  orderedTodos,
  planStatus,
  source,
  sourceChangeId,
  sourceRevision,
  resuming,
  error,
  onResume,
  className,
}: {
  orderedTodos: NonNullable<ReturnType<typeof useTodosStore.getState>['snapshot']>['todos'];
  planStatus: PlanStatus;
  source?: string;
  sourceChangeId?: string;
  sourceRevision?: number;
  resuming: boolean;
  error: string | null;
  onResume: () => void;
  className: string;
}) {
  const { t } = useTranslation();

  return (
    <div className="px-3 pb-3 pt-2">
      {planStatus === 'paused' && (
        <div className="mb-3 rounded-md border border-amber-300/60 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-400/30 dark:bg-amber-400/10 dark:text-amber-200">
          <div className="flex items-center gap-2">
            <span className="min-w-0 flex-1">{t('todos.pausedHint')}</span>
            <button
              type="button"
              onClick={onResume}
              disabled={resuming}
              className="inline-flex shrink-0 items-center gap-1 rounded-md border border-amber-400/50 bg-white/80 px-2 py-1 text-[11px] font-semibold text-amber-900 transition hover:bg-white disabled:cursor-not-allowed disabled:opacity-60 dark:border-amber-300/30 dark:bg-amber-300/10 dark:text-amber-100 dark:hover:bg-amber-300/20"
            >
              <Play className="h-3 w-3" />
              {resuming ? t('todos.resumeRunning') : t('todos.resume')}
            </button>
          </div>
          {error && <div className="mt-2 text-[11px] text-red-700 dark:text-red-300">{error}</div>}
        </div>
      )}

      {orderedTodos.length > 0 ? (
        <ol className={`space-y-1 overflow-y-auto pr-1 ${className}`}>
          {orderedTodos.map((todo) => (
            <TodoItem
              key={todo.id}
              todo={todo}
              source={source}
              sourceChangeId={sourceChangeId}
              sourceRevision={sourceRevision}
            />
          ))}
        </ol>
      ) : (
        <p className="py-2 text-xs text-[var(--text-secondary)]">
          {t('todos.emptyActivePlan')}
        </p>
      )}
    </div>
  );
}

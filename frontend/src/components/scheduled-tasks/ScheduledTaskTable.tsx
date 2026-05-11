import { Fragment, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import cronstrue from 'cronstrue/i18n';
import 'cronstrue/locales/zh_CN';
import {
  AlertTriangle,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Clock3,
  History,
  Loader2,
  Pencil,
  Play,
  Power,
  PowerOff,
  Trash2,
  XCircle,
} from 'lucide-react';
import type { ScheduledTask, ScheduledTaskRun, ScheduledTaskRunStatus } from '../../types/api';
import { formatDateTime } from '../../utils/date';

interface Props {
  tasks: ScheduledTask[];
  expandedTaskId: string | null;
  runsByTaskId: Record<string, ScheduledTaskRun[]>;
  runsLoadingByTaskId: Record<string, boolean>;
  runningNowTaskId: string | null;
  onToggleExpanded: (task: ScheduledTask) => void;
  onOpenRuns: (task: ScheduledTask) => void;
  onEdit: (task: ScheduledTask) => void;
  onRunNow: (task: ScheduledTask) => void;
  onDelete: (task: ScheduledTask) => void;
  onToggleEnabled: (task: ScheduledTask, enabled: boolean) => void;
}

function statusClass(status: ScheduledTaskRunStatus): string {
  switch (status) {
    case 'succeeded':
      return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300';
    case 'running':
      return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300';
    case 'skipped':
      return 'bg-[var(--bg-secondary)] text-[var(--text-secondary)]';
    case 'timeout':
      return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300';
    case 'failed':
      return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300';
  }
}

function cronLocaleFrom(language?: string): string {
  return (language || 'zh').startsWith('zh') ? 'zh_CN' : 'en';
}

function formatSchedule(task: ScheduledTask, everyLabel: string, notScheduledLabel: string, locale?: string): string {
  if (task.cron_expr) {
    const cronLocale = cronLocaleFrom(locale);
    try {
      return `${cronstrue.toString(task.cron_expr, { locale: cronLocale, use24HourTimeFormat: true })} · ${task.timezone || 'UTC'}`;
    } catch {
      return `${task.cron_expr} · ${task.timezone || 'UTC'}`;
    }
  }
  if (task.interval_sec) return `${everyLabel} ${formatInterval(task.interval_sec)}`;
  return notScheduledLabel;
}

function formatInterval(seconds: number): string {
  if (seconds % 86400 === 0) return `${seconds / 86400}d`;
  if (seconds % 3600 === 0) return `${seconds / 3600}h`;
  if (seconds % 60 === 0) return `${seconds / 60}m`;
  return `${seconds}s`;
}

function isInteractiveTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return Boolean(target.closest('button,a,input,select,textarea'));
}

export function ScheduledTaskTable({
  tasks,
  expandedTaskId,
  runsByTaskId,
  runsLoadingByTaskId,
  runningNowTaskId,
  onToggleExpanded,
  onOpenRuns,
  onEdit,
  onRunNow,
  onDelete,
  onToggleEnabled,
}: Props) {
  const { i18n, t } = useTranslation();
  const statusLabel = (status: ScheduledTaskRunStatus) => t(`scheduledTasks.status.${status}`, status);
  const everyLabel = t('scheduledTasks.every', '每隔');
  const notScheduledLabel = t('scheduledTasks.notScheduled', '未调度');

  const handleKeyDown = (event: KeyboardEvent<HTMLTableRowElement>, task: ScheduledTask) => {
    if (isInteractiveTarget(event.target)) return;
    const key = event.key.toLowerCase();
    if (key === 'enter') {
      event.preventDefault();
      onToggleExpanded(task);
      return;
    }
    if (key === 'e') {
      event.preventDefault();
      onEdit(task);
      return;
    }
    if (key === 'r') {
      event.preventDefault();
      onRunNow(task);
      return;
    }
    if (event.key === 'Delete') {
      event.preventDefault();
      if (window.confirm(t('scheduledTasks.deleteConfirm', '确定删除定时任务 "{{name}}"？', { name: task.name }))) {
        onDelete(task);
      }
    }
  };

  return (
    <div className="overflow-hidden rounded-2xl border border-[var(--border-color)] bg-[var(--bg-card)] shadow-sm">
      <table role="table" className="min-w-full border-collapse text-left text-sm">
        <thead className="border-b border-[var(--border-color)] bg-[var(--bg-secondary)] text-xs uppercase text-[var(--text-secondary)]">
          <tr>
            <th className="w-10 px-4 py-3" aria-label={t('common.expand', '展开')} />
            <th scope="col" className="px-4 py-3 font-medium">{t('scheduledTasks.task', '任务')}</th>
            <th scope="col" className="px-4 py-3 font-medium">{t('scheduledTasks.target', '目标')}</th>
            <th scope="col" className="px-4 py-3 font-medium">{t('scheduledTasks.schedule', '调度')}</th>
            <th scope="col" className="px-4 py-3 font-medium">{t('scheduledTasks.nextRun', '下次运行')}</th>
            <th scope="col" className="px-4 py-3 font-medium">{t('scheduledTasks.lastRun', '最近运行')}</th>
            <th scope="col" className="px-4 py-3 text-right font-medium">{t('scheduledTasks.actions', '操作')}</th>
          </tr>
        </thead>
        <tbody>
          {tasks.map((task) => {
            const expanded = expandedTaskId === task.id;
            const runs = runsByTaskId[task.id] ?? [];
            const previewRuns = runs.slice(0, 5);
            const runsLoading = runsLoadingByTaskId[task.id] ?? false;
            const runningNow = runningNowTaskId === task.id;
            return (
              <Fragment key={task.id}>
                <tr
                  tabIndex={0}
                  onKeyDown={(event) => handleKeyDown(event, task)}
                  className="group border-b border-[var(--border-color)] outline-none transition-colors hover:bg-[var(--bg-hover)] focus-visible:bg-[var(--accent-50)] focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--accent-500)] dark:focus-visible:bg-[var(--accent-light)]"
                  aria-label={t('scheduledTasks.rowAria', '{{name}}，按 Enter 展开运行记录，E 编辑，R 立即运行，Delete 删除', { name: task.name })}
                >
                  <td className="px-4 py-4 align-top">
                    <button
                      type="button"
                      onClick={() => onToggleExpanded(task)}
                      className="rounded-md p-1 text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-secondary)] hover:text-[var(--text-primary)]"
                      aria-label={expanded ? t('common.collapse', '收起') : t('common.expand', '展开')}
                      title={expanded ? t('common.collapse', '收起') : t('common.expand', '展开')}
                    >
                      {expanded ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
                    </button>
                  </td>
                  <td className="max-w-[280px] px-4 py-4 align-top">
                    <div className="flex min-w-0 items-center gap-2">
                      <span className="truncate font-medium text-[var(--text-primary)]">{task.name}</span>
                      <span className={`shrink-0 rounded-full px-2 py-0.5 text-[11px] font-medium ${
                        task.enabled
                          ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
                          : 'bg-[var(--bg-secondary)] text-[var(--text-secondary)]'
                      }`}>
                        {task.enabled ? t('scheduledTasks.enabled', '已启用') : t('scheduledTasks.disabled', '已禁用')}
                      </span>
                      {task.active_run_id && (
                        <span className="inline-flex shrink-0 items-center gap-1 rounded-full bg-blue-100 px-2 py-0.5 text-[11px] font-medium text-blue-700 dark:bg-blue-900/30 dark:text-blue-300">
                          <Clock3 className="h-3 w-3" />
                          {t('scheduledTasks.running', '运行中')}
                        </span>
                      )}
                    </div>
                    {task.description && (
                      <p className="mt-1 line-clamp-2 text-xs leading-5 text-[var(--text-secondary)]">{task.description}</p>
                    )}
                    {task.last_error && (
                      <p className="mt-2 flex items-start gap-1.5 text-xs leading-5 text-red-600 dark:text-red-300">
                        <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                        <span className="line-clamp-2">{task.last_error}</span>
                      </p>
                    )}
                  </td>
                  <td className="px-4 py-4 align-top">
                    <div className="text-sm text-[var(--text-primary)]">
                      {task.target_type === 'session' ? t('scheduledTasks.targetSession', 'Web Session') : t('scheduledTasks.targetImPush', 'IM 推送')}
                    </div>
                    {task.platform && <div className="mt-1 text-xs text-[var(--text-secondary)]">{task.platform}</div>}
                  </td>
                  <td className="px-4 py-4 align-top">
                    <div className="text-xs text-[var(--text-primary)]">{formatSchedule(task, everyLabel, notScheduledLabel, i18n.language)}</div>
                    {task.cron_expr && <div className="mt-1 font-mono text-[11px] text-[var(--text-secondary)]">{task.cron_expr}</div>}
                  </td>
                  <td className="whitespace-nowrap px-4 py-4 align-top text-xs text-[var(--text-secondary)]">
                    {task.next_run_at ? formatDateTime(task.next_run_at) : t('scheduledTasks.notScheduled', '未调度')}
                  </td>
                  <td className="whitespace-nowrap px-4 py-4 align-top text-xs text-[var(--text-secondary)]">
                    {task.last_run_at ? formatDateTime(task.last_run_at) : t('scheduledTasks.neverRun', '从未运行')}
                  </td>
                  <td className="px-4 py-4 align-top">
                    <div className="flex justify-end gap-1">
                      <IconButton
                        label={task.enabled ? t('scheduledTasks.disable', '禁用') : t('scheduledTasks.enable', '启用')}
                        onClick={() => onToggleEnabled(task, !task.enabled)}
                      >
                        {task.enabled ? <PowerOff className="h-4 w-4" /> : <Power className="h-4 w-4" />}
                      </IconButton>
                      <IconButton label={t('scheduledTasks.viewRuns', '运行记录')} onClick={() => onOpenRuns(task)}>
                        <History className="h-4 w-4" />
                      </IconButton>
                      <IconButton label={t('scheduledTasks.runNow', '立即运行')} onClick={() => onRunNow(task)} disabled={runningNow}>
                        {runningNow ? <Loader2 className="h-4 w-4 animate-spin" /> : <Play className="h-4 w-4" />}
                      </IconButton>
                      <IconButton label={t('common.edit', '编辑')} onClick={() => onEdit(task)}>
                        <Pencil className="h-4 w-4" />
                      </IconButton>
                      <IconButton
                        label={t('common.delete', '删除')}
                        danger
                        onClick={() => {
                          if (window.confirm(t('scheduledTasks.deleteConfirm', '确定删除定时任务 "{{name}}"？', { name: task.name }))) {
                            onDelete(task);
                          }
                        }}
                      >
                        <Trash2 className="h-4 w-4" />
                      </IconButton>
                    </div>
                  </td>
                </tr>
                {expanded && (
                  <tr key={`${task.id}-runs`} className="border-b border-[var(--border-color)] bg-[var(--bg-primary)]/60">
                    <td />
                    <td colSpan={6} className="px-4 py-4">
                      <div className="rounded-xl border border-[var(--border-color)] bg-[var(--bg-card)] p-3">
                        <div className="mb-2 flex items-center justify-between">
                          <span className="text-xs font-medium text-[var(--text-secondary)]">
                            {t('scheduledTasks.recentRuns', '最近 5 条运行记录')}
                          </span>
                          <button
                            type="button"
                            onClick={() => onOpenRuns(task)}
                            className="text-xs font-medium text-[var(--accent-600)] hover:text-[var(--accent-700)] dark:text-[var(--accent-300)]"
                          >
                            {t('scheduledTasks.viewAllRuns', '查看全部')}
                          </button>
                        </div>
                        {runsLoading ? (
                          <div className="py-4 text-center text-xs text-[var(--text-secondary)]">
                            {t('common.loading', '加载中...')}
                          </div>
                        ) : previewRuns.length === 0 ? (
                          <div className="py-4 text-center text-xs text-[var(--text-secondary)]">
                            {t('scheduledTasks.noRuns', '暂无运行记录')}
                          </div>
                        ) : (
                          <div className="space-y-2">
                            {previewRuns.map((run) => (
                              <RunPreview key={`${run.scheduled_at}-${run.id}`} run={run} statusLabel={statusLabel(run.status)} />
                            ))}
                          </div>
                        )}
                      </div>
                    </td>
                  </tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function IconButton({ label, onClick, children, disabled, danger }: {
  label: string;
  onClick: () => void;
  children: React.ReactNode;
  disabled?: boolean;
  danger?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className={`rounded-lg p-2 transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${
        danger
          ? 'text-[var(--text-secondary)] hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20'
          : 'text-[var(--text-secondary)] hover:bg-[var(--bg-secondary)] hover:text-[var(--text-primary)]'
      }`}
      aria-label={label}
      title={label}
    >
      {children}
    </button>
  );
}

function RunPreview({ run, statusLabel }: { run: ScheduledTaskRun; statusLabel: string }) {
  return (
    <div className="grid grid-cols-[150px_110px_1fr] items-start gap-3 rounded-lg bg-[var(--bg-primary)] px-3 py-2 text-xs">
      <div className="whitespace-nowrap text-[var(--text-secondary)]">{formatDateTime(run.started_at)}</div>
      <div>
        <span className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 font-medium ${statusClass(run.status)}`}>
          {run.status === 'succeeded' ? <CheckCircle2 className="h-3 w-3" /> : run.status === 'failed' ? <XCircle className="h-3 w-3" /> : null}
          {statusLabel}
        </span>
      </div>
      <div className="line-clamp-2 text-[var(--text-secondary)]">
        {run.error || run.output || run.session_id || run.id}
      </div>
    </div>
  );
}

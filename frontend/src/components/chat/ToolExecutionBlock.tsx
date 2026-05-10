import { useId, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { FileText, Check, X, Loader2 } from 'lucide-react';
import { useChatStore } from '../../store/chat';
import { getToolDisplayName } from '../../utils/toolName';

interface ToolExecutionBlockProps {
  id: string;
  name: string;
  args: string;
  result?: string;
  status?: 'running' | 'success' | 'error';
  duration?: number;
  isError?: boolean;
}

export function ToolExecutionBlock({
  id,
  name,
  args,
  result,
  status,
  duration,
  isError,
}: ToolExecutionBlockProps) {
  const { t } = useTranslation();
  const domId = useId();
  const detailId = `tool-exec-detail-${domId}`;

  const liveStatus = useChatStore((s) => s.toolCallStatuses?.[id]);
  const resolvedDuration = liveStatus?.duration ?? duration;
  const requiresUserApproval = liveStatus?.requires_user_approval === true;

  const resolvedStatus = useMemo<'running' | 'success' | 'error'>(() => {
    if (liveStatus?.status) return liveStatus.status;
    if (status) return status;
    if (isError) return 'error';
    return 'success';
  }, [liveStatus?.status, status, isError]);

  const [expanded, setExpanded] = useState(false);
  const displayName = getToolDisplayName(name, t);
  const isRunning = resolvedStatus === 'running';
  const isErrState = resolvedStatus === 'error';

  const argsFormatted = useMemo(() => {
    if (!args) return '';
    try {
      const parsed = JSON.parse(args);
      if (Object.keys(parsed).length > 0) {
        return JSON.stringify(parsed, null, 2);
      }
      return '';
    } catch {
      if (args === '{}' || args === 'null' || /^map\[.*\]$/.test(args.trim())) {
        return '';
      }
      return args;
    }
  }, [args]);

  const durationText = useMemo(() => {
    // 运行中且尚无时长 → D7 要求显示 `--` 占位，保证宽度稳定
    if (resolvedDuration === undefined) {
      return isRunning ? '--' : null;
    }
    return resolvedDuration >= 1000
      ? `${(resolvedDuration / 1000).toFixed(1)}s`
      : `${Math.round(resolvedDuration)}ms`;
  }, [resolvedDuration, isRunning]);

  const toggleLabel = expanded ? t('tools.clickToCollapse') : t('tools.clickToExpand');

  return (
    <div
      className={`rounded-lg border border-[var(--border-color)] bg-[var(--bg-card)] overflow-hidden${
        isErrState ? ' ring-1 ring-[var(--danger)]/30' : ''
      }`}
    >
      <div className="flex items-center gap-2 px-3 py-2 min-h-[44px]">
        <FileText
          className="w-4 h-4 shrink-0"
          style={{ color: isErrState ? 'var(--danger)' : 'var(--accent-600)' }}
          aria-hidden="true"
        />
        <span className="text-[13px] font-semibold text-[var(--text-primary)] truncate">
          {displayName}
        </span>
        {isRunning && (
          <span className="text-[11px] text-[var(--text-secondary)] shrink-0">
            {t('chat.generating')}
          </span>
        )}
        {isErrState && requiresUserApproval && (
          <span className="text-[11px] font-medium text-[var(--danger)] shrink-0">
            Permission required
          </span>
        )}
        <div className="flex items-center gap-2 ml-auto shrink-0">
          {isRunning && (
            <Loader2
              className="w-3.5 h-3.5 animate-spin"
              style={{ color: 'var(--accent-600)' }}
              aria-hidden="true"
            />
          )}
          {resolvedStatus === 'success' && (
            <Check
              className="w-3.5 h-3.5"
              style={{ color: 'var(--success)' }}
              aria-hidden="true"
            />
          )}
          {isErrState && (
            <X
              className="w-3.5 h-3.5"
              style={{ color: 'var(--danger)' }}
              aria-hidden="true"
            />
          )}
          {durationText && (
            <span className="text-[11px] font-medium text-[var(--text-secondary)]">
              {durationText}
            </span>
          )}
          <button
            type="button"
            onClick={() => !isRunning && setExpanded(!expanded)}
            disabled={isRunning}
            aria-expanded={expanded}
            aria-controls={detailId}
            className={`text-[11px] font-medium text-[var(--text-secondary)] transition-colors ${
              isRunning
                ? 'opacity-50 cursor-not-allowed'
                : 'hover:underline'
            }`}
          >
            {toggleLabel}
          </button>
        </div>
      </div>

      {expanded && (
        <div
          id={detailId}
          className="border-t border-[var(--border-color)] px-3 py-2.5 space-y-2.5 bg-[var(--bg-secondary)]"
        >
          {argsFormatted && (
            <div>
              <div className="text-[11px] font-semibold text-[var(--accent-600)] dark:text-[var(--accent-300)] mb-1">
                {t('tools.input')}
              </div>
              <pre className="text-[12px] font-mono whitespace-pre-wrap break-words text-[var(--text-primary)] bg-[var(--bg-card)] border border-[var(--border-color)] rounded p-2">
                {argsFormatted}
              </pre>
            </div>
          )}
          <div>
            <div
              className={`text-[11px] font-semibold mb-1 ${
                isErrState
                  ? 'text-[var(--danger)]'
                  : 'text-[var(--success)]'
              }`}
            >
              {t('tools.output')}
            </div>
            <pre className="text-[12px] font-mono whitespace-pre-wrap break-words text-[var(--text-primary)] bg-[var(--bg-card)] border border-[var(--border-color)] rounded p-2">
              {result
                ? result.length > 2000
                  ? result.slice(0, 2000) + '\n' + t('tools.truncated')
                  : result
                : isRunning
                  ? '…'
                  : ''}
            </pre>
          </div>
        </div>
      )}
    </div>
  );
}

export default ToolExecutionBlock;

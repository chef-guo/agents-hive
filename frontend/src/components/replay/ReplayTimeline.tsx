import { useReplayStore } from '../../store/replay';
import type { JournalEvent, JournalEventType } from '../../types/journal';
import type { SessionTraceResponse, TraceTimelineItem } from '../../types/api';

const typeLabels: Record<JournalEventType, string> = {
  tool_call: '工具调用',
  file_change: '文件变更',
  decision: '决策',
};

const typeIcons: Record<JournalEventType, string> = {
  tool_call: 'T',
  file_change: 'F',
  decision: 'D',
};

function eventLabel(e: JournalEvent): string {
  if (e.type === 'decision') return e.decision?.slice(0, 40) || '决策';
  if (e.type === 'file_change') return `${e.action} ${e.file_path?.split('/').pop() || ''}`;
  return e.tool_name || 'tool_call';
}

interface ReplayTimelineProps {
  trace?: SessionTraceResponse | null;
  selectedTraceIndex?: number | null;
  onSelectTrace?: (index: number) => void;
  onSelectJournal?: () => void;
}

const traceKindLabels: Record<string, string> = {
  span: 'Span',
  quality_event: '质量',
};

export function ReplayTimeline({ trace, selectedTraceIndex = null, onSelectTrace, onSelectJournal }: ReplayTimelineProps) {
  const events = useReplayStore((s) => s.events);
  const filteredIndices = useReplayStore((s) => s.filteredIndices);
  const currentIndex = useReplayStore((s) => s.currentIndex);
  const filterType = useReplayStore((s) => s.filterType);
  const setFilterType = useReplayStore((s) => s.setFilterType);
  const setCurrentIndex = useReplayStore((s) => s.setCurrentIndex);
  const pause = useReplayStore((s) => s.pause);

  const filters: (JournalEventType | null)[] = [null, 'tool_call', 'file_change', 'decision'];
  const traceItems = trace?.items || [];

  return (
    <div className="replay-timeline" style={{ display: 'flex', flexDirection: 'column', height: '100%', minWidth: 220 }}>
      {/* Filter chips */}
      <div style={{ display: 'flex', gap: 6, padding: '8px 12px', flexWrap: 'wrap' }}>
        {filters.map((f) => (
          <button
            key={f ?? 'all'}
            onClick={() => setFilterType(f)}
            style={{
              padding: '4px 10px',
              borderRadius: 10,
              border: '1px solid var(--accent-border, rgba(59,130,246,0.2))',
              background: filterType === f ? 'var(--accent-subtle, rgba(59,130,246,0.08))' : 'transparent',
              color: filterType === f ? 'var(--accent-600, #2563EB)' : 'var(--text-secondary, #6C6C70)',
              fontSize: 12,
              cursor: 'pointer',
              fontFamily: 'DM Sans, sans-serif',
            }}
          >
            {f ? `${typeIcons[f]} ${typeLabels[f]}` : '全部'}
          </button>
        ))}
      </div>

      {/* Event list */}
      <div style={{ flex: 1, overflowY: 'auto', padding: '0 8px' }}>
        {filteredIndices.map((eventIdx, i) => {
          const e = events[eventIdx];
          const isActive = i === currentIndex;
          return (
            <button
              key={i}
              onClick={() => { onSelectJournal?.(); setCurrentIndex(i); pause(); }}
              tabIndex={0}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 8,
                width: '100%',
                padding: '8px 10px',
                borderRadius: 8,
                border: 'none',
                background: isActive ? 'var(--accent-subtle, rgba(59,130,246,0.08))' : 'transparent',
                cursor: 'pointer',
                textAlign: 'left',
                fontFamily: 'DM Sans, sans-serif',
                fontSize: 13,
                color: isActive ? 'var(--accent-600, #2563EB)' : 'var(--text-primary, #1C1C1E)',
                transition: 'background 150ms',
              }}
            >
              <span style={{ fontSize: 14, flexShrink: 0 }}>{typeIcons[e.type]}</span>
              <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {eventLabel(e)}
              </span>
              {e.duration_ms != null && e.duration_ms > 0 && (
                <span style={{ fontSize: 11, color: 'var(--text-secondary, #6C6C70)', flexShrink: 0 }}>
                  {e.duration_ms}ms
                </span>
              )}
              {e.is_error && <span style={{ color: '#DC2626', fontSize: 11 }}>✗</span>}
            </button>
          );
        })}
      </div>

      <div style={{ borderTop: '1px solid var(--border, rgba(0,0,0,0.08))', padding: '8px 8px 10px' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, margin: '0 4px 6px' }}>
          <span style={{ fontSize: 12, fontWeight: 700, color: 'var(--text-primary, #1C1C1E)' }}>Trace</span>
          <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text-secondary, #6C6C70)' }}>
            {traceItems.length}
          </span>
        </div>
        <div style={{ maxHeight: 220, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 2 }}>
          {traceItems.length === 0 ? (
            <div style={{ padding: '6px 4px', fontSize: 12, color: 'var(--text-secondary, #6C6C70)' }}>
              暂无 trace 数据
            </div>
          ) : traceItems.map((item, i) => {
            const isActive = i === selectedTraceIndex;
            return (
              <button
                key={`${item.trace_id || 'trace'}-${item.span_id || i}-${i}`}
                onClick={() => onSelectTrace?.(i)}
                style={{
                  display: 'grid',
                  gridTemplateColumns: '44px minmax(0, 1fr) auto',
                  alignItems: 'center',
                  gap: 8,
                  width: '100%',
                  padding: '7px 8px',
                  borderRadius: 8,
                  border: 'none',
                  background: isActive ? 'var(--accent-subtle, rgba(59,130,246,0.08))' : 'transparent',
                  cursor: 'pointer',
                  textAlign: 'left',
                  fontFamily: 'DM Sans, sans-serif',
                  color: isActive ? 'var(--accent-600, #2563EB)' : 'var(--text-primary, #1C1C1E)',
                }}
              >
                <span style={{
                  fontSize: 10,
                  color: traceKindColor(item),
                  border: `1px solid ${traceKindBorder(item)}`,
                  borderRadius: 999,
                  padding: '1px 5px',
                  textAlign: 'center',
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                }}>
                  {traceLabel(item)}
                </span>
                <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontSize: 12 }}>
                  {item.operation || item.kind}
                </span>
                {item.duration_ms != null && item.duration_ms > 0 && (
                  <span style={{ fontSize: 11, color: 'var(--text-secondary, #6C6C70)' }}>{item.duration_ms}ms</span>
                )}
              </button>
            );
          })}
        </div>
      </div>
    </div>
  );
}

function traceLabel(item: TraceTimelineItem): string {
  if (item.operation === 'quality.reflection') return '反思';
  return traceKindLabels[item.kind] || item.kind;
}

function traceKindColor(item: TraceTimelineItem): string {
  if (item.operation === 'quality.reflection') return '#D97706';
  if (item.kind === 'quality_event') return '#2563EB';
  return 'var(--text-secondary, #6C6C70)';
}

function traceKindBorder(item: TraceTimelineItem): string {
  if (item.operation === 'quality.reflection') return 'rgba(217,119,6,0.25)';
  if (item.kind === 'quality_event') return 'rgba(37,99,235,0.25)';
  return 'var(--border, rgba(0,0,0,0.08))';
}

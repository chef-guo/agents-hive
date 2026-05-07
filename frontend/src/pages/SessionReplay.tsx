import { useEffect, useRef, useState } from 'react';
import { useParams, useNavigate, useSearchParams } from 'react-router-dom';
import { useReplayStore } from '../store/replay';
import { useNodeClient } from '../hooks/useNodeClient';
import { useReplayWebSocket } from '../hooks/useReplayWebSocket';
import { attachQualityEvent, getCharacterState } from '../types/journal';
import type { Message, SessionTraceResponse } from '../types/api';
import { AgentCharacter } from '../components/replay/AgentCharacter';
import { ReplayTimeline } from '../components/replay/ReplayTimeline';
import { ReplayControls } from '../components/replay/ReplayControls';
import { EventDetailPanel } from '../components/replay/EventDetailPanel';
import { ReplayStats } from '../components/replay/ReplayStats';
import { AgentTreeView } from '../components/replay/AgentTreeView';
import { buildQualityCandidateRequest } from '../components/replay/qualityCandidate';
import { useAuthStore } from '../store/auth';
import { useToastStore } from '../store/toast';
import { ArrowLeft } from 'lucide-react';

export function SessionReplay() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const [initialStepParam] = useState(() => searchParams.get('step'));
  const client = useNodeClient();

  // Live 模式 WebSocket 连接（独立连接，带 sessionId 隔离）
  const wsUrl = client ? client.getWebSocketUrl() : '';
  useReplayWebSocket({
    url: wsUrl,
    sessionId: id || '',
    enabled: !!id && !!client,
  });

  const mode = useReplayStore((s) => s.mode);
  const events = useReplayStore((s) => s.events);
  const filteredIndices = useReplayStore((s) => s.filteredIndices);
  const currentIndex = useReplayStore((s) => s.currentIndex);
  const speed = useReplayStore((s) => s.speed);
  const setEvents = useReplayStore((s) => s.setEvents);
  const setMode = useReplayStore((s) => s.setMode);
  const setError = useReplayStore((s) => s.setError);
  const setSessionId = useReplayStore((s) => s.setSessionId);
  const setCurrentIndex = useReplayStore((s) => s.setCurrentIndex);
  const reset = useReplayStore((s) => s.reset);

  const timerRef = useRef<ReturnType<typeof setTimeout>>(undefined);
  const [sessionName, setSessionName] = useState('');
  const [sessionDate, setSessionDate] = useState('');
  const [startedAt, setStartedAt] = useState<string | undefined>();
  const [endedAt, setEndedAt] = useState<string | undefined>();
  const [messages, setMessages] = useState<Message[]>([]);
  const [trace, setTrace] = useState<SessionTraceResponse | null>(null);
  const [traceError, setTraceError] = useState('');
  const [selectedTraceIndex, setSelectedTraceIndex] = useState<number | null>(null);
  const [candidateBusy, setCandidateBusy] = useState(false);
  const currentUser = useAuthStore((s) => s.user);
  const addToast = useToastStore((s) => s.addToast);

  // 加载数据
  useEffect(() => {
    if (!id || !client) return;
    reset();
    setSessionId(id);
    setTrace(null);
    setTraceError('');
    setSelectedTraceIndex(null);

    const load = async () => {
      try {
        // 并行加载 session 元数据和 journal 事件
        const [session, journal, sessionMessages, sessionTrace] = await Promise.all([
          client.getSession(id),
          client.getSessionJournal(id),
          client.getMessages(id).catch(() => []),
          client.getSessionTrace(id).catch((err) => {
            setTraceError(err instanceof Error ? err.message : 'Trace 加载失败');
            return null;
          }),
        ]);
        setSessionName(session.name || '未命名会话');
        setSessionDate(session.created || session.last_accessed);
        setMessages(sessionMessages);
        setTrace(sessionTrace);
        setSelectedTraceIndex(null);
        const eventsWithQuality = journal.events.map(attachQualityEvent);
        setEvents(eventsWithQuality);

        if (eventsWithQuality.length > 0) {
          setStartedAt(eventsWithQuality[0].timestamp);
          setEndedAt(eventsWithQuality[eventsWithQuality.length - 1].timestamp);
        }

        // URL 深链接：?step=N
        if (initialStepParam) {
          const step = parseInt(initialStepParam, 10);
          if (!isNaN(step) && step >= 0 && step < eventsWithQuality.length) {
            setCurrentIndex(step);
          }
        }
      } catch (err) {
        setError(err instanceof Error ? err.message : '加载失败');
      }
    };
    load();

    return () => { reset(); };
  }, [id, client, initialStepParam, reset, setCurrentIndex, setError, setEvents, setSessionId]);

  // 播放引擎
  useEffect(() => {
    if (mode !== 'playing' || filteredIndices.length === 0) return;

    const eventIdx = filteredIndices[currentIndex];
    const nextFilteredIdx = currentIndex + 1;

    if (nextFilteredIdx >= filteredIndices.length) {
      setMode('paused');
      return;
    }

    const currentEvent = events[eventIdx];
    const nextEvent = events[filteredIndices[nextFilteredIdx]];
    const gap = new Date(nextEvent.timestamp).getTime() - new Date(currentEvent.timestamp).getTime();
    const delay = Math.max(500, Math.min(3000, gap)) / speed;

    timerRef.current = setTimeout(() => {
      setCurrentIndex(currentIndex + 1);
    }, delay);

    return () => clearTimeout(timerRef.current);
  }, [mode, currentIndex, filteredIndices, events, speed, setCurrentIndex, setMode]);

  // URL 深链接 debounced replaceState
  const urlTimer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => {
    clearTimeout(urlTimer.current);
    urlTimer.current = setTimeout(() => {
      const realIdx = filteredIndices[currentIndex] ?? currentIndex;
      setSearchParams({ step: String(realIdx) }, { replace: true });
    }, 500);
    return () => clearTimeout(urlTimer.current);
  }, [currentIndex, filteredIndices, setSearchParams]);

  // 当前事件
  const currentEventIdx = filteredIndices[currentIndex];
  const currentEvent = currentEventIdx != null ? events[currentEventIdx] : null;
  const selectedTraceItem = selectedTraceIndex != null ? trace?.items[selectedTraceIndex] ?? null : null;
  const charState = currentEvent ? getCharacterState(currentEvent) : 'idle';
  const canCreateQualityCandidate = currentUser?.role === 'admin';

  const createQualityCandidate = async () => {
    if (!id || !currentEvent || currentEventIdx == null) return;
    const req = buildQualityCandidateRequest(id, currentEvent, currentEventIdx, messages);
    if (!req) {
      addToast('warning', '当前事件不是可沉淀的失败质量事件，或找不到对应用户输入');
      return;
    }
    setCandidateBusy(true);
    try {
      const created = await client.adminCreateQualityCandidate(req);
      addToast('success', `已写入候选池：${created.id}`);
    } catch (err) {
      addToast('error', err instanceof Error ? err.message : '写入候选池失败');
    } finally {
      setCandidateBusy(false);
    }
  };

  // 思考泡泡
  const bubbleText = currentEvent?.type === 'decision'
    ? currentEvent.decision
    : currentEvent?.type === 'tool_call'
      ? currentEvent.tool_name
      : null;

  if (mode === 'loading') {
    return (
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100vh', fontFamily: 'DM Sans, sans-serif', color: 'var(--text-secondary, #6C6C70)' }}>
        加载中...
      </div>
    );
  }

  if (mode === 'error') {
    return (
      <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', height: '100vh', gap: 16, fontFamily: 'DM Sans, sans-serif' }}>
        <div style={{ color: '#DC2626' }}>{useReplayStore.getState().errorMessage}</div>
        <button onClick={() => navigate(-1)} style={{ padding: '8px 16px', borderRadius: 10, border: '1px solid var(--accent-border)', background: 'transparent', cursor: 'pointer' }}>
          返回
        </button>
      </div>
    );
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100vh', background: 'var(--bg-primary, #F2F2F7)' }}>
      {/* Header */}
      <div style={{
        display: 'flex',
        alignItems: 'center',
        gap: 12,
        padding: '12px 20px',
        borderBottom: '1px solid var(--border, rgba(0,0,0,0.08))',
        background: 'var(--bg-card, #fff)',
        fontFamily: 'DM Sans, sans-serif',
      }}>
        <button onClick={() => navigate(-1)} aria-label="返回" style={{ background: 'none', border: 'none', cursor: 'pointer', padding: 4 }}>
          <ArrowLeft size={20} color="var(--text-primary, #1C1C1E)" />
        </button>
        <span style={{ fontSize: 16, fontWeight: 600, fontFamily: 'Geist, sans-serif', color: 'var(--text-primary, #1C1C1E)' }}>
          {sessionName}
        </span>
        <span style={{ fontSize: 12, color: 'var(--text-secondary, #6C6C70)', marginLeft: 'auto' }}>
          {sessionDate ? new Date(sessionDate).toLocaleDateString() : ''}
        </span>
        {mode === 'live' && (
          <span style={{ fontSize: 11, color: '#DC2626', fontWeight: 600, padding: '2px 8px', background: '#FEF2F2', borderRadius: 6 }}>
            LIVE
          </span>
        )}
      </div>

      {/* Stats */}
      <ReplayStats events={events} startedAt={startedAt} endedAt={endedAt} />

      {/* Main content */}
      <div style={{ flex: 1, display: 'flex', overflow: 'hidden' }}>
        {/* 左侧时间轴 */}
        <div style={{ width: 280, borderRight: '1px solid var(--border, rgba(0,0,0,0.08))', background: 'var(--bg-card, #fff)', overflow: 'hidden' }}>
          <ReplayTimeline
            trace={trace}
            selectedTraceIndex={selectedTraceIndex}
            onSelectTrace={setSelectedTraceIndex}
            onSelectJournal={() => setSelectedTraceIndex(null)}
          />
        </div>

        {/* 右侧舞台 + 详情 */}
        <div style={{ flex: 1, display: 'flex', flexDirection: 'column' }}>
          {/* 舞台 */}
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', position: 'relative' }}>
            <AgentCharacter state={charState} size={160} />
            {/* 思考泡泡 */}
            {bubbleText && (
              <div style={{
                position: 'absolute',
                top: '15%',
                left: '55%',
                maxWidth: 260,
                padding: '10px 14px',
                background: 'var(--bg-card, #fff)',
                borderRadius: 12,
                boxShadow: '0 2px 8px rgba(0,0,0,0.08)',
                fontSize: 13,
                fontFamily: 'DM Sans, sans-serif',
                color: 'var(--text-primary, #1C1C1E)',
                wordBreak: 'break-word',
              }}>
                {bubbleText.length > 80 ? bubbleText.slice(0, 80) + '...' : bubbleText}
                <div style={{
                  position: 'absolute',
                  bottom: -6,
                  left: 20,
                  width: 12,
                  height: 12,
                  background: 'var(--bg-card, #fff)',
                  transform: 'rotate(45deg)',
                  boxShadow: '2px 2px 4px rgba(0,0,0,0.04)',
                }} />
              </div>
            )}

            {/* 空状态 */}
            {mode === 'empty' && (
              <div style={{
                position: 'absolute',
                bottom: '20%',
                textAlign: 'center',
                fontFamily: 'DM Sans, sans-serif',
                color: 'var(--text-secondary, #6C6C70)',
              }}>
                <div style={{ fontSize: 14, marginBottom: 8 }}>这个 session 还没有执行记录</div>
                <button onClick={() => navigate(-1)} style={{
                  padding: '6px 16px',
                  borderRadius: 10,
                  border: '1px solid var(--accent-border)',
                  background: 'transparent',
                  cursor: 'pointer',
                  fontSize: 13,
                  color: 'var(--accent-600, #1D4ED8)',
                }}>
                  返回
                </button>
              </div>
            )}
          </div>

          {/* 事件详情面板 */}
          <div style={{
            height: 200,
            borderTop: '1px solid var(--border, rgba(0,0,0,0.08))',
            background: 'var(--bg-card, #fff)',
            overflow: 'hidden',
          }}>
            <EventDetailPanel
              event={currentEvent}
              traceItem={selectedTraceItem}
              canCreateCandidate={canCreateQualityCandidate}
              candidateBusy={candidateBusy}
              onCreateCandidate={createQualityCandidate}
            />
          </div>
        </div>

        <div style={{
          width: 280,
          borderLeft: '1px solid var(--border, rgba(0,0,0,0.08))',
          background: 'var(--bg-card, #fff)',
          overflowY: 'auto',
        }}>
          {traceError ? (
            <div style={{ padding: 12, fontSize: 12, color: '#D97706', fontFamily: 'DM Sans, sans-serif' }}>
              {traceError}
            </div>
          ) : (
            <AgentTreeView nodes={trace?.agent_tree || []} />
          )}
        </div>
      </div>

      {/* 控制条 */}
      <ReplayControls />
    </div>
  );
}

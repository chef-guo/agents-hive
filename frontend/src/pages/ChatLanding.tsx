import { useState, useEffect, useCallback, useRef } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { MessageSquare } from 'lucide-react';
import { HiveLogo } from '../layouts/Sidebar';
import { useSessionStore } from '../store/session';
import { useChatStore } from '../store/chat';
import { useNodeClient } from '../hooks/useNodeClient';
import { ChatInput, type SendOptions } from '../components/chat/ChatInput';
import { SessionKBBar } from '../components/chat/SessionKBBar';
import type { KBBinding, KBNamespace } from '../types/api';
import { buildSessionKBNamespaceIDs } from './chatKBUtils';

function normalizeKBDomain(value: string | undefined) {
  return value?.trim() || 'generic';
}

export function ChatLanding() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const client = useNodeClient();
  const sessions = useSessionStore((s) => s.sessions);
  const fetchSessions = useSessionStore((s) => s.fetchSessions);
  const createSession = useSessionStore((s) => s.createSession);
  const loadModels = useChatStore((s) => s.loadModels);
  const [draftSessionId, setDraftSessionId] = useState<string | null>(null);
  const draftSessionIdRef = useRef<string | null>(null);
  const draftSessionPromiseRef = useRef<Promise<string> | null>(null);
  const [sending, setSending] = useState(false);
  const [kbNamespaces, setKBNamespace] = useState<KBNamespace[]>([]);
  const [kbBindings, setKBBindings] = useState<KBBinding[]>([]);
  const [kbSelection, setKBSelection] = useState<string[]>([]);
  const [kbDomainId, setKBDomainId] = useState('generic');
  const [kbBusy, setKBBusy] = useState(false);

  useEffect(() => {
    fetchSessions(client);
  }, [client, fetchSessions]);

  useEffect(() => {
    draftSessionIdRef.current = draftSessionId;
  }, [draftSessionId]);

  useEffect(() => {
    loadModels(client, draftSessionId ?? undefined);
  }, [client, draftSessionId, loadModels]);

  const loadKBState = useCallback(async (sessionId: string | null, requestedDomainId: string) => {
    const domainId = normalizeKBDomain(requestedDomainId);
    try {
      const [namespacesRes, bindingsRes] = await Promise.all([
        client.listKBNamespaces({ domainId, limit: 100 }),
        sessionId
          ? client.getSessionKBBindings(sessionId, domainId)
          : Promise.resolve({ bindings: [] as KBBinding[] }),
      ]);
      const namespaces = namespacesRes.namespaces ?? [];
      const bindings = bindingsRes.bindings ?? [];
      const boundIDs = new Set(bindings.map((binding) => binding.namespace_id));
      const availableIDs = new Set(namespaces.map((namespace) => namespace.id));
      setKBNamespace(namespaces);
      setKBBindings(bindings);
      setKBSelection((current) => current.filter((namespaceID) => availableIDs.has(namespaceID) && !boundIDs.has(namespaceID)));
    } catch {
      setKBNamespace([]);
      setKBBindings([]);
      setKBSelection([]);
    }
  }, [client]);

  useEffect(() => {
    void loadKBState(draftSessionId, kbDomainId);
  }, [draftSessionId, kbDomainId, loadKBState]);

  const ensureDraftSession = useCallback(async (name?: string) => {
    if (draftSessionIdRef.current) return draftSessionIdRef.current;
    if (draftSessionPromiseRef.current) return draftSessionPromiseRef.current;

    draftSessionPromiseRef.current = (async () => {
      const emptySession = sessions.find((s) => s.message_count === 0);
      const id = emptySession
        ? emptySession.id
        : await createSession(client, name?.trim().slice(0, 30) || t('sessions.newSession', '新会话'));
      draftSessionIdRef.current = id;
      setDraftSessionId(id);
      return id;
    })().finally(() => {
      draftSessionPromiseRef.current = null;
    });

    return draftSessionPromiseRef.current;
  }, [client, createSession, sessions, t]);

  const handleSend = async (content: string, options?: SendOptions) => {
    const text = content.trim();
    if (!text || sending) return;

    setSending(true);
    try {
      const id = await ensureDraftSession(text);
      const domainId = normalizeKBDomain(kbDomainId);
      const nextNamespaceIds = buildSessionKBNamespaceIDs(kbBindings, kbSelection);
      if (nextNamespaceIds.length > 0) {
        await client.setSessionKBBindings(id, nextNamespaceIds, domainId);
      }
      navigate(`/sessions/${id}`, {
        state: {
          pendingMessage: text,
          pendingOptions: options,
          pendingKBDomainId: domainId,
        },
      });
    } catch {
      // 错误已在 store 中处理
    }
    setSending(false);
  };

  const handleKBDomainChange = useCallback((domainId: string) => {
    const normalized = normalizeKBDomain(domainId);
    setKBDomainId(normalized);
    setKBSelection([]);
    if (draftSessionId) {
      client.updateSession(draftSessionId, { kb_domain_id: normalized }).catch(() => {});
    }
  }, [client, draftSessionId]);

  const handleAddKB = useCallback(async () => {
    if (kbSelection.length === 0) return;
    setKBBusy(true);
    try {
      const domainId = normalizeKBDomain(kbDomainId);
      const id = await ensureDraftSession();
      const nextNamespaceIds = buildSessionKBNamespaceIDs(kbBindings, kbSelection);
      await client.setSessionKBBindings(id, nextNamespaceIds, domainId);
      setKBSelection([]);
      await loadKBState(id, domainId);
    } finally {
      setKBBusy(false);
    }
  }, [client, ensureDraftSession, kbBindings, kbDomainId, kbSelection, loadKBState]);

  const handleRemoveKB = useCallback(async (namespaceId: string) => {
    if (!draftSessionId) return;
    setKBBusy(true);
    try {
      const domainId = normalizeKBDomain(kbDomainId);
      await client.deleteSessionKBBinding(draftSessionId, namespaceId, domainId);
      await loadKBState(draftSessionId, domainId);
    } finally {
      setKBBusy(false);
    }
  }, [client, draftSessionId, kbDomainId, loadKBState]);

  const recentSessions = sessions.filter((s) => s.message_count > 0).slice(0, 5);

  return (
    <div className="flex h-full flex-col items-center justify-center px-4">
      <div className="w-full max-w-4xl text-center">
        {/* Logo + 欢迎文案 */}
        <HiveLogo className="w-14 h-14 mx-auto mb-4" />
        <h1 className="text-xl font-semibold text-[var(--text-primary)] mb-1 font-display">
          {t('chatLanding.title')}
        </h1>
        <p className="text-sm text-[var(--text-secondary)] mb-8">
          {t('chatLanding.subtitle')}
        </p>

        <div className="text-left">
          <SessionKBBar
            namespaces={kbNamespaces}
            bindings={kbBindings}
            value={kbSelection}
            domainId={kbDomainId}
            busy={kbBusy || sending}
            onChange={setKBSelection}
            onDomainChange={handleKBDomainChange}
            onReload={() => { void loadKBState(draftSessionId, kbDomainId); }}
            onAdd={handleAddKB}
            onRemove={handleRemoveKB}
          />
          <ChatInput
            sessionId={draftSessionId ?? ''}
            onRequireSession={() => ensureDraftSession()}
            onSend={handleSend}
            disabled={sending}
            allowAttachments
            allowDeepThinking
          />
        </div>

        {/* 最近会话 */}
        {recentSessions.length > 0 && (
          <div className="mt-8">
            <div className="flex items-center gap-1.5 justify-center mb-3">
              <MessageSquare className="w-3 h-3 text-[var(--text-secondary)]" />
              <span className="text-xs text-[var(--text-secondary)]">
                {t('chatLanding.recentSessions')}
              </span>
            </div>
            <div className="flex flex-wrap gap-2 justify-center">
              {recentSessions.map((s) => (
                <button
                  key={s.id}
                  onClick={() => navigate(`/sessions/${s.id}`)}
                  className="px-3 py-1.5 text-xs rounded-lg border border-[var(--border-color)] bg-[var(--bg-card)] text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:border-[var(--accent-300)] dark:hover:border-[var(--accent-700)] transition-colors truncate max-w-[200px]"
                >
                  {s.name || s.id.slice(0, 8)}
                </button>
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

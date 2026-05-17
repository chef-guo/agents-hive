import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Archive, BookOpenText, Database, Eye, FileUp, Link2, Plus, RefreshCw, Search, ShieldOff, UploadCloud } from 'lucide-react';
import { Streamdown } from 'streamdown';
import { useNodeClient } from '../../hooks/useNodeClient';
import { useToastStore } from '../../store/toast';
import { ALLOWED_TAGS, STREAMDOWN_PREVIEW_PLUGINS } from '../../utils/streamdownConfig';
import type {
  KBBinding,
  KBBindingType,
  KBDocument,
  KBDocumentStatus,
  KBEffectiveBindingsFilter,
  KBIngestReport,
  KBMarkdownPreviewAsset,
  KBNamespace,
  KBStructureNode,
} from '../../types/api';

type TabKey = 'namespaces' | 'documents' | 'bindings';

const bindingTypes: KBBindingType[] = ['agent', 'domain', 'session_template', 'session', 'tenant', 'user', 'system'];
const documentStatuses: Array<KBDocumentStatus | ''> = ['', 'active', 'draft', 'archived', 'revoked'];

export function KnowledgeBase() {
  const { t } = useTranslation();
  const client = useNodeClient();
  const addToast = useToastStore((s) => s.addToast);
  const [domainId, setDomainId] = useState('generic');
  const [activeTab, setActiveTab] = useState<TabKey>('namespaces');
  const [namespaceQuery, setNamespaceQuery] = useState('');
  const [namespaces, setNamespaces] = useState<KBNamespace[]>([]);
  const [selectedNamespaceId, setSelectedNamespaceId] = useState('');
  const [documents, setDocuments] = useState<KBDocument[]>([]);
  const [documentQuery, setDocumentQuery] = useState('');
  const [documentStatus, setDocumentStatus] = useState<KBDocumentStatus | ''>('');
  const [bindings, setBindings] = useState<KBBinding[]>([]);
  const [effectiveBindings, setEffectiveBindings] = useState<KBBinding[]>([]);
  const [effectiveFilter, setEffectiveFilter] = useState<KBEffectiveBindingsFilter>({ domainId: 'generic' });
  const [bindingEnabled, setBindingEnabled] = useState<'all' | 'true' | 'false'>('all');
  const [tree, setTree] = useState<KBStructureNode[]>([]);
  const [selectedDocument, setSelectedDocument] = useState<KBDocument | null>(null);
  const [lastIngestReport, setLastIngestReport] = useState<KBIngestReport | null>(null);
  const [loading, setLoading] = useState(false);
  const [namespaceModalOpen, setNamespaceModalOpen] = useState(false);
  const [ingestModalOpen, setIngestModalOpen] = useState(false);
  const [bindingModalOpen, setBindingModalOpen] = useState(false);

  const selectedNamespace = useMemo(
    () => namespaces.find((item) => item.id === selectedNamespaceId) ?? null,
    [namespaces, selectedNamespaceId],
  );

  const loadNamespaces = useCallback(async () => {
    setLoading(true);
    try {
      const res = await client.listKBNamespaces({ domainId, query: namespaceQuery, limit: 100 });
      const items = res.namespaces ?? [];
      setNamespaces(items);
      if (!selectedNamespaceId && items.length > 0) setSelectedNamespaceId(items[0].id);
      if (selectedNamespaceId && !items.some((item) => item.id === selectedNamespaceId)) {
        setSelectedNamespaceId(items[0]?.id ?? '');
      }
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : t('knowledgeBase.errors.loadNamespaces'));
    } finally {
      setLoading(false);
    }
  }, [client, domainId, namespaceQuery, selectedNamespaceId, addToast, t]);

  const loadDocuments = useCallback(async () => {
    if (!selectedNamespaceId) {
      setDocuments([]);
      return;
    }
    setLoading(true);
    try {
      const res = await client.listKBDocuments(selectedNamespaceId, {
        domainId,
        query: documentQuery,
        status: documentStatus,
        limit: 100,
      });
      setDocuments(res.documents ?? []);
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : t('knowledgeBase.errors.loadDocuments'));
    } finally {
      setLoading(false);
    }
  }, [client, domainId, selectedNamespaceId, documentQuery, documentStatus, addToast, t]);

  const loadBindings = useCallback(async () => {
    setLoading(true);
    try {
      const res = await client.listKBBindings({
        domainId,
        namespaceId: selectedNamespaceId || undefined,
        enabled: bindingEnabled === 'all' ? undefined : bindingEnabled === 'true',
      });
      setBindings(res.bindings ?? []);
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : t('knowledgeBase.errors.loadBindings'));
    } finally {
      setLoading(false);
    }
  }, [client, domainId, selectedNamespaceId, bindingEnabled, addToast, t]);

  const previewEffectiveBindings = useCallback(async (filter: KBEffectiveBindingsFilter = effectiveFilter) => {
    setLoading(true);
    try {
      const res = await client.getKBEffectiveBindings({
        ...filter,
        domainId: filter.domainId?.trim() || domainId,
      });
      setEffectiveBindings(res.bindings ?? []);
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : t('knowledgeBase.errors.previewEffectiveBindings'));
    } finally {
      setLoading(false);
    }
  }, [client, domainId, effectiveFilter, addToast, t]);

  useEffect(() => { loadNamespaces(); }, [loadNamespaces]);
  useEffect(() => { loadDocuments(); }, [loadDocuments]);
  useEffect(() => { loadBindings(); }, [loadBindings]);
  useEffect(() => { setEffectiveFilter((prev) => ({ ...prev, domainId })); }, [domainId]);

  const refreshAll = async () => {
    await Promise.all([loadNamespaces(), loadDocuments(), loadBindings()]);
  };

  const handleTree = async (doc: KBDocument) => {
    setSelectedDocument(doc);
    try {
      const res = await client.getKBDocumentTree(doc.id, domainId);
      setTree(res.nodes ?? []);
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : t('knowledgeBase.errors.loadTree'));
    }
  };

  const handleArchive = async (doc: KBDocument) => {
    if (!window.confirm(t('knowledgeBase.confirm.archiveDocument', { title: doc.title }))) return;
    try {
      await client.archiveKBDocument(doc.id, domainId);
      addToast('success', t('knowledgeBase.toasts.documentArchived'));
      await loadDocuments();
      if (selectedDocument?.id === doc.id) {
        setSelectedDocument(null);
        setTree([]);
      }
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : t('knowledgeBase.errors.archiveDocument'));
    }
  };

  const handleDisableBinding = async (binding: KBBinding) => {
    if (!window.confirm(t('knowledgeBase.confirm.disableBinding', { type: binding.binding_type, target: binding.binding_target }))) return;
    try {
      await client.disableKBBinding(binding.id, domainId);
      addToast('success', t('knowledgeBase.toasts.bindingDisabled'));
      await loadBindings();
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : t('knowledgeBase.errors.disableBinding'));
    }
  };

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex flex-col gap-4 mb-6 md:flex-row md:items-end md:justify-between">
        <div>
          <h1 className="text-xl font-semibold text-[var(--text-primary)] font-display">{t('knowledgeBase.title')}</h1>
          <p className="mt-1 text-sm text-[var(--text-secondary)]">{t('knowledgeBase.subtitle')}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <label className="text-xs text-[var(--text-secondary)]">
            {t('knowledgeBase.domain')}
            <input
              value={domainId}
              onChange={(e) => setDomainId(e.target.value.trim() || 'generic')}
              className="ml-2 w-40 rounded-lg border border-[var(--border-color)] bg-[var(--bg-card)] px-3 py-2 text-sm text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)]"
            />
          </label>
          <IconButton icon={RefreshCw} label={t('common.refresh')} onClick={refreshAll} disabled={loading} />
          <button
            onClick={() => setNamespaceModalOpen(true)}
            className="inline-flex items-center gap-1.5 rounded-lg bg-[var(--accent-600)] px-3 py-2 text-sm text-white transition-colors hover:bg-[var(--accent-700)]"
          >
            <Plus className="h-4 w-4" />
            {t('knowledgeBase.actions.createNamespace')}
          </button>
        </div>
      </div>

      <div className="mb-5 flex gap-4 border-b border-[var(--border-color)]">
        {[
          { key: 'namespaces' as TabKey, label: t('knowledgeBase.tabs.namespaces'), icon: Database },
          { key: 'documents' as TabKey, label: t('knowledgeBase.tabs.documents'), icon: BookOpenText },
          { key: 'bindings' as TabKey, label: t('knowledgeBase.tabs.bindings'), icon: Link2 },
        ].map((tab) => (
          <button
            key={tab.key}
            onClick={() => setActiveTab(tab.key)}
            className={`inline-flex items-center gap-1.5 border-b-2 px-3 py-2 text-sm font-medium transition-colors ${
              activeTab === tab.key
                ? 'border-[var(--accent-600)] text-[var(--accent-600)]'
                : 'border-transparent text-[var(--text-secondary)] hover:text-[var(--text-primary)]'
            }`}
          >
            <tab.icon className="h-4 w-4" />
            {tab.label}
          </button>
        ))}
      </div>

      {activeTab === 'namespaces' && (
        <section className="space-y-4">
          <ToolbarSearch value={namespaceQuery} onChange={setNamespaceQuery} placeholder={t('knowledgeBase.placeholders.searchNamespaces')} />
          <div className="overflow-hidden rounded-xl border border-[var(--border-color)]">
            <table className="w-full text-sm">
              <thead className="bg-[var(--bg-secondary)] text-left text-[var(--text-secondary)]">
                <tr>
                  <th className="px-4 py-3 font-medium">{t('knowledgeBase.columns.name')}</th>
                  <th className="px-4 py-3 font-medium">{t('knowledgeBase.columns.id')}</th>
                  <th className="px-4 py-3 font-medium">{t('knowledgeBase.columns.strategy')}</th>
                  <th className="px-4 py-3 font-medium">{t('knowledgeBase.columns.longSectionSummary')}</th>
                  <th className="px-4 py-3 text-right font-medium">{t('knowledgeBase.columns.actions')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-[var(--border-color)]">
                {namespaces.length === 0 ? (
                  <EmptyRow colSpan={5} text={t('knowledgeBase.empty.namespaces')} />
                ) : namespaces.map((namespace) => (
                  <tr key={namespace.id} className={namespace.id === selectedNamespaceId ? 'bg-[var(--accent-50)]/50 dark:bg-[var(--accent-light)]' : 'hover:bg-[var(--bg-secondary)]'}>
                    <td className="px-4 py-3 font-medium text-[var(--text-primary)]">{namespace.name}</td>
                    <td className="px-4 py-3 font-mono text-xs text-[var(--text-secondary)]">{namespace.id}</td>
                    <td className="px-4 py-3 text-[var(--text-secondary)]">{namespace.index_strategy}</td>
                    <td className="px-4 py-3 text-[var(--text-secondary)]">
                      {namespace.thinning_enabled
                        ? t('knowledgeBase.namespace.longSectionThreshold', { count: namespace.thinning_token_threshold })
                        : t('knowledgeBase.namespace.longSectionDisabled')} / {t('knowledgeBase.namespace.summaryThreshold', { count: namespace.summary_token_threshold })}
                    </td>
                    <td className="px-4 py-3 text-right">
                      <button
                        onClick={() => { setSelectedNamespaceId(namespace.id); setActiveTab('documents'); }}
                        className="rounded-lg border border-[var(--border-color)] px-2.5 py-1.5 text-xs text-[var(--text-primary)] hover:bg-[var(--bg-secondary)]"
                      >
                        {t('knowledgeBase.actions.openDocuments')}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}

      {activeTab === 'documents' && (
        <section className="grid gap-4 lg:grid-cols-[minmax(0,1.15fr)_minmax(360px,0.85fr)]">
          <div className="space-y-4">
            <DocumentControls
              namespaces={namespaces}
              selectedNamespaceId={selectedNamespaceId}
              documentQuery={documentQuery}
              documentStatus={documentStatus}
              onNamespaceChange={setSelectedNamespaceId}
              onQueryChange={setDocumentQuery}
              onStatusChange={setDocumentStatus}
              onIngest={() => setIngestModalOpen(true)}
            />
            <div className="overflow-hidden rounded-xl border border-[var(--border-color)]">
              <table className="w-full text-sm">
                <thead className="bg-[var(--bg-secondary)] text-left text-[var(--text-secondary)]">
                  <tr>
                    <th className="px-4 py-3 font-medium">{t('knowledgeBase.columns.title')}</th>
                    <th className="px-4 py-3 font-medium">{t('knowledgeBase.columns.version')}</th>
                    <th className="px-4 py-3 font-medium">{t('knowledgeBase.columns.status')}</th>
                    <th className="px-4 py-3 font-medium">{t('knowledgeBase.columns.effectiveAt')}</th>
                    <th className="px-4 py-3 text-right font-medium">{t('knowledgeBase.columns.actions')}</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-[var(--border-color)]">
                  {documents.length === 0 ? (
                    <EmptyRow colSpan={5} text={selectedNamespaceId ? t('knowledgeBase.empty.documents') : t('knowledgeBase.empty.selectNamespaceFirst')} />
                  ) : documents.map((doc) => (
                    <tr key={doc.id} className={selectedDocument?.id === doc.id ? 'bg-[var(--accent-50)]/50 dark:bg-[var(--accent-light)]' : 'hover:bg-[var(--bg-secondary)]'}>
                      <td className="px-4 py-3">
                        <div className="font-medium text-[var(--text-primary)]">{doc.title}</div>
                        <div className="mt-0.5 truncate font-mono text-[11px] text-[var(--text-secondary)]">{doc.id}</div>
                      </td>
                      <td className="px-4 py-3 text-[var(--text-secondary)]">{doc.version}</td>
                      <td className="px-4 py-3"><StatusBadge status={doc.status} /></td>
                      <td className="px-4 py-3 text-[var(--text-secondary)]">{formatTime(doc.effective_at)}</td>
                      <td className="px-4 py-3">
                        <div className="flex justify-end gap-2">
                          <button onClick={() => handleTree(doc)} className="rounded-lg border border-[var(--border-color)] px-2.5 py-1.5 text-xs text-[var(--text-primary)] hover:bg-[var(--bg-secondary)]">
                            {t('knowledgeBase.actions.viewStructure')}
                          </button>
                          {doc.status !== 'archived' && (
                            <button onClick={() => handleArchive(doc)} className="inline-flex items-center gap-1 rounded-lg border border-red-200 px-2.5 py-1.5 text-xs text-red-600 hover:bg-red-50 dark:border-red-800 dark:hover:bg-red-900/20">
                              <Archive className="h-3.5 w-3.5" />
                              {t('knowledgeBase.actions.archive')}
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
          <TreePanel document={selectedDocument} nodes={tree} />
        </section>
      )}

      {activeTab === 'bindings' && (
        <section className="space-y-4">
          <EffectiveBindingsPreview
            filter={effectiveFilter}
            bindings={effectiveBindings}
            loading={loading}
            onFilterChange={setEffectiveFilter}
            onPreview={previewEffectiveBindings}
          />
          <div className="flex flex-wrap items-center gap-2">
            <select value={selectedNamespaceId} onChange={(e) => setSelectedNamespaceId(e.target.value)} className="rounded-lg border border-[var(--border-color)] bg-[var(--bg-card)] px-3 py-2 text-sm text-[var(--text-primary)]">
              <option value="">{t('knowledgeBase.filters.allNamespaces')}</option>
              {namespaces.map((namespace) => <option key={namespace.id} value={namespace.id}>{namespace.name}</option>)}
            </select>
            <select value={bindingEnabled} onChange={(e) => setBindingEnabled(e.target.value as 'all' | 'true' | 'false')} className="rounded-lg border border-[var(--border-color)] bg-[var(--bg-card)] px-3 py-2 text-sm text-[var(--text-primary)]">
              <option value="all">{t('knowledgeBase.filters.allStatuses')}</option>
              <option value="true">{t('knowledgeBase.status.enabled')}</option>
              <option value="false">{t('knowledgeBase.status.disabled')}</option>
            </select>
            <button
              onClick={() => setBindingModalOpen(true)}
              disabled={namespaces.length === 0}
              className="inline-flex items-center gap-1.5 rounded-lg bg-[var(--accent-600)] px-3 py-2 text-sm text-white transition-colors hover:bg-[var(--accent-700)] disabled:opacity-50"
            >
              <Plus className="h-4 w-4" />
              {t('knowledgeBase.actions.createBinding')}
            </button>
          </div>
          <div className="overflow-hidden rounded-xl border border-[var(--border-color)]">
            <table className="w-full text-sm">
              <thead className="bg-[var(--bg-secondary)] text-left text-[var(--text-secondary)]">
                <tr>
                  <th className="px-4 py-3 font-medium">{t('knowledgeBase.columns.namespace')}</th>
                  <th className="px-4 py-3 font-medium">{t('knowledgeBase.columns.type')}</th>
                  <th className="px-4 py-3 font-medium">{t('knowledgeBase.columns.target')}</th>
                  <th className="px-4 py-3 font-medium">{t('knowledgeBase.columns.status')}</th>
                  <th className="px-4 py-3 font-medium">{t('knowledgeBase.columns.effectiveExpires')}</th>
                  <th className="px-4 py-3 text-right font-medium">{t('knowledgeBase.columns.actions')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-[var(--border-color)]">
                {bindings.length === 0 ? (
                  <EmptyRow colSpan={6} text={t('knowledgeBase.empty.bindings')} />
                ) : bindings.map((binding) => (
                  <tr key={binding.id} className="hover:bg-[var(--bg-secondary)]">
                    <td className="px-4 py-3 font-mono text-xs text-[var(--text-secondary)]">{binding.namespace_id}</td>
                    <td className="px-4 py-3 text-[var(--text-primary)]">{t(`knowledgeBase.bindingTypes.${binding.binding_type}`)}</td>
                    <td className="px-4 py-3 font-mono text-xs text-[var(--text-secondary)]">{binding.binding_target}</td>
                    <td className="px-4 py-3">{binding.enabled ? <StatusPill tone="green" label={t('knowledgeBase.status.enabled')} /> : <StatusPill tone="gray" label={t('knowledgeBase.status.disabled')} />}</td>
                    <td className="px-4 py-3 text-[var(--text-secondary)]">{formatTime(binding.effective_at)} / {binding.expires_at ? formatTime(binding.expires_at) : t('knowledgeBase.status.longTerm')}</td>
                    <td className="px-4 py-3 text-right">
                      {binding.enabled && (
                        <button onClick={() => handleDisableBinding(binding)} className="inline-flex items-center gap-1 rounded-lg border border-red-200 px-2.5 py-1.5 text-xs text-red-600 hover:bg-red-50 dark:border-red-800 dark:hover:bg-red-900/20">
                          <ShieldOff className="h-3.5 w-3.5" />
                          {t('knowledgeBase.actions.disable')}
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}

      {namespaceModalOpen && (
        <NamespaceModal
          domainId={domainId}
          onClose={() => setNamespaceModalOpen(false)}
          onSubmit={async (body) => {
            const namespace = await client.createKBNamespace(body, domainId);
            addToast('success', t('knowledgeBase.toasts.namespaceCreated'));
            setNamespaceModalOpen(false);
            setSelectedNamespaceId(namespace.id);
            await loadNamespaces();
          }}
        />
      )}

      {ingestModalOpen && selectedNamespace && (
        <IngestModal
          namespace={selectedNamespace}
          domainId={domainId}
          onPreview={async (body) => client.previewKBMarkdown(selectedNamespace.id, body, domainId)}
          onClose={() => setIngestModalOpen(false)}
          onSubmit={async (body) => {
            const res = await client.ingestKBDocument(selectedNamespace.id, body, domainId);
            setLastIngestReport(res.report ?? null);
            addToast(res.warnings?.length ? 'warning' : 'success', res.warnings?.length ? t('knowledgeBase.toasts.documentImportedWithWarnings', { warnings: res.warnings.join(', ') }) : t('knowledgeBase.toasts.documentImported'));
            setIngestModalOpen(false);
            await loadDocuments();
          }}
        />
      )}

      {bindingModalOpen && (
        <BindingModal
          namespaces={namespaces}
          selectedNamespaceId={selectedNamespaceId}
          domainId={domainId}
          onClose={() => setBindingModalOpen(false)}
          onSubmit={async (body) => {
            await client.createKBBinding(body, domainId);
            addToast('success', t('knowledgeBase.toasts.bindingCreated'));
            setBindingModalOpen(false);
            await loadBindings();
          }}
        />
      )}
      {lastIngestReport && (
        <IngestReportPanel report={lastIngestReport} onClose={() => setLastIngestReport(null)} />
      )}
    </div>
  );
}

function ToolbarSearch({ value, onChange, placeholder }: { value: string; onChange: (value: string) => void; placeholder: string }) {
  return (
    <div className="relative max-w-sm">
      <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-[var(--text-secondary)]" />
      <input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="w-full rounded-lg border border-[var(--border-color)] bg-[var(--bg-card)] py-2 pl-9 pr-3 text-sm text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)]"
      />
    </div>
  );
}

function DocumentControls(props: {
  namespaces: KBNamespace[];
  selectedNamespaceId: string;
  documentQuery: string;
  documentStatus: KBDocumentStatus | '';
  onNamespaceChange: (value: string) => void;
  onQueryChange: (value: string) => void;
  onStatusChange: (value: KBDocumentStatus | '') => void;
  onIngest: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-wrap items-center gap-2">
      <select value={props.selectedNamespaceId} onChange={(e) => props.onNamespaceChange(e.target.value)} className="rounded-lg border border-[var(--border-color)] bg-[var(--bg-card)] px-3 py-2 text-sm text-[var(--text-primary)]">
        <option value="">{t('knowledgeBase.placeholders.selectNamespace')}</option>
        {props.namespaces.map((namespace) => <option key={namespace.id} value={namespace.id}>{namespace.name}</option>)}
      </select>
      <select value={props.documentStatus} onChange={(e) => props.onStatusChange(e.target.value as KBDocumentStatus | '')} className="rounded-lg border border-[var(--border-color)] bg-[var(--bg-card)] px-3 py-2 text-sm text-[var(--text-primary)]">
        {documentStatuses.map((status) => <option key={status || 'all'} value={status}>{status ? t(`knowledgeBase.documentStatuses.${status}`) : t('knowledgeBase.filters.allStatuses')}</option>)}
      </select>
      <ToolbarSearch value={props.documentQuery} onChange={props.onQueryChange} placeholder={t('knowledgeBase.placeholders.searchDocuments')} />
      <button
        onClick={props.onIngest}
        disabled={!props.selectedNamespaceId}
        className="inline-flex items-center gap-1.5 rounded-lg bg-[var(--accent-600)] px-3 py-2 text-sm text-white transition-colors hover:bg-[var(--accent-700)] disabled:opacity-50"
      >
        <FileUp className="h-4 w-4" />
        {t('knowledgeBase.actions.importDocument')}
      </button>
    </div>
  );
}

function EffectiveBindingsPreview(props: {
  filter: KBEffectiveBindingsFilter;
  bindings: KBBinding[];
  loading: boolean;
  onFilterChange: React.Dispatch<React.SetStateAction<KBEffectiveBindingsFilter>>;
  onPreview: (filter?: KBEffectiveBindingsFilter) => Promise<void>;
}) {
  const { t } = useTranslation();
  const update = (key: keyof KBEffectiveBindingsFilter, value: string) => {
    props.onFilterChange((prev) => ({ ...prev, [key]: value }));
  };
  return (
    <div className="rounded-xl border border-[var(--border-color)] bg-[var(--bg-card)] p-4">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="text-sm font-semibold text-[var(--text-primary)]">{t('knowledgeBase.effective.title')}</div>
          <div className="mt-0.5 text-xs text-[var(--text-secondary)]">{t('knowledgeBase.effective.subtitle')}</div>
        </div>
        <button
          onClick={() => props.onPreview(props.filter)}
          disabled={props.loading}
          className="inline-flex items-center gap-1.5 rounded-lg border border-[var(--border-color)] px-3 py-2 text-sm text-[var(--text-primary)] hover:bg-[var(--bg-secondary)] disabled:opacity-50"
        >
          <Eye className="h-4 w-4" />
          {t('knowledgeBase.actions.preview')}
        </button>
      </div>
      <div className="grid gap-3 md:grid-cols-3">
        <TextField label={t('knowledgeBase.runtimeFields.agentId')} value={props.filter.agentId ?? ''} onChange={(value) => update('agentId', value)} />
        <TextField label={t('knowledgeBase.runtimeFields.domainId')} value={props.filter.domainId ?? ''} onChange={(value) => update('domainId', value)} />
        <TextField label={t('knowledgeBase.runtimeFields.sessionTemplateId')} value={props.filter.sessionTemplateId ?? ''} onChange={(value) => update('sessionTemplateId', value)} />
        <TextField label={t('knowledgeBase.runtimeFields.sessionId')} value={props.filter.sessionId ?? ''} onChange={(value) => update('sessionId', value)} />
        <TextField label={t('knowledgeBase.runtimeFields.tenantId')} value={props.filter.tenantId ?? ''} onChange={(value) => update('tenantId', value)} />
      </div>
      <div className="mt-4 overflow-hidden rounded-lg border border-[var(--border-color)]">
        <table className="w-full text-sm">
          <thead className="bg-[var(--bg-secondary)] text-left text-[var(--text-secondary)]">
            <tr>
              <th className="px-3 py-2 font-medium">{t('knowledgeBase.columns.namespace')}</th>
              <th className="px-3 py-2 font-medium">{t('knowledgeBase.columns.source')}</th>
              <th className="px-3 py-2 font-medium">{t('knowledgeBase.columns.effectiveExpires')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[var(--border-color)]">
            {props.bindings.length === 0 ? (
              <EmptyRow colSpan={3} text={t('knowledgeBase.empty.effectiveBindings')} />
            ) : props.bindings.map((binding) => (
              <tr key={binding.id} className="hover:bg-[var(--bg-secondary)]">
                <td className="px-3 py-2 font-mono text-xs text-[var(--text-secondary)]">{binding.namespace_id}</td>
                <td className="px-3 py-2">
                  <div className="text-[var(--text-primary)]">{t(`knowledgeBase.bindingTypes.${binding.binding_type}`)}:{binding.binding_target}</div>
                  <div className="mt-0.5 font-mono text-[10px] text-[var(--text-secondary)]">{binding.id}</div>
                </td>
                <td className="px-3 py-2 text-[var(--text-secondary)]">{formatTime(binding.effective_at)} / {binding.expires_at ? formatTime(binding.expires_at) : t('knowledgeBase.status.longTerm')}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function TreePanel({ document, nodes }: { document: KBDocument | null; nodes: KBStructureNode[] }) {
  const { t } = useTranslation();
  return (
    <aside className="min-h-[420px] rounded-xl border border-[var(--border-color)] bg-[var(--bg-card)]">
      <div className="border-b border-[var(--border-color)] px-4 py-3">
        <div className="text-sm font-semibold text-[var(--text-primary)]">{document ? document.title : t('knowledgeBase.tree.title')}</div>
        <div className="mt-0.5 truncate font-mono text-[11px] text-[var(--text-secondary)]">{document?.id ?? t('knowledgeBase.tree.selectHint')}</div>
      </div>
      <div className="max-h-[620px] overflow-auto p-3">
        {nodes.length === 0 ? (
          <div className="py-12 text-center text-sm text-[var(--text-secondary)]">{t('knowledgeBase.empty.tree')}</div>
        ) : nodes.map((node) => <TreeNodeView key={node.id} node={node} />)}
      </div>
    </aside>
  );
}

function TreeNodeView({ node }: { node: KBStructureNode }) {
  const { t } = useTranslation();
  return (
    <div className="ml-3 border-l border-[var(--border-color)] pl-3">
      <div className="rounded-lg px-2 py-1.5 hover:bg-[var(--bg-secondary)]">
        <div className="flex items-center justify-between gap-2">
          <span className="text-sm font-medium text-[var(--text-primary)]">{node.title}</span>
          <span className="font-mono text-[10px] text-[var(--text-secondary)]">{node.id}</span>
        </div>
        <div className="mt-0.5 text-[11px] text-[var(--text-secondary)]">
          {t('knowledgeBase.tree.path')} {node.node_path || '-'} · {t('knowledgeBase.tree.tokens', { count: node.token_count })} · {t('knowledgeBase.tree.lines', { start: node.start_line, end: node.end_line })}
        </div>
        {node.summary && <div className="mt-1 text-xs text-[var(--text-secondary)]">{node.summary}</div>}
      </div>
      {node.children?.map((child) => <TreeNodeView key={child.id} node={child} />)}
    </div>
  );
}

function IngestReportPanel({ report, onClose }: { report: KBIngestReport; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <div className="mt-6 rounded-xl border border-[var(--border-color)] bg-[var(--bg-card)] p-4">
      <div className="mb-3 flex items-start justify-between gap-3">
        <div>
          <div className="text-sm font-semibold text-[var(--text-primary)]">{t('knowledgeBase.report.title')}</div>
          <div className="mt-0.5 font-mono text-[11px] text-[var(--text-secondary)]">{report.ingest_id} · {report.document_id || '-'}</div>
        </div>
        <button onClick={onClose} className="text-xs text-[var(--text-secondary)] hover:text-[var(--text-primary)]">{t('common.close')}</button>
      </div>
      <div className="grid gap-2 text-xs md:grid-cols-4">
        <ReportMetric label={t('knowledgeBase.report.metrics.sections')} value={report.tree_nodes} />
        <ReportMetric label={t('knowledgeBase.report.metrics.imageRefs')} value={report.image_refs} />
        <ReportMetric label={t('knowledgeBase.report.metrics.boundAssets')} value={report.bound_assets} />
        <ReportMetric label={t('knowledgeBase.report.metrics.unboundAssets')} value={report.unbound_assets} tone={report.unbound_assets > 0 ? 'warn' : 'normal'} />
        <ReportMetric label={t('knowledgeBase.report.metrics.markdownLines')} value={report.markdown_lines} />
        <ReportMetric label={t('knowledgeBase.report.metrics.uploadedAssets')} value={report.uploaded_assets} />
        <ReportMetric label={t('knowledgeBase.report.metrics.convertedAssets')} value={report.converted_assets} />
        <ReportMetric label={t('knowledgeBase.report.metrics.durationMs')} value={report.duration_ms} />
      </div>
      {(report.provider || report.quality || report.warnings?.length) && (
        <div className="mt-3 rounded-lg bg-[var(--bg-secondary)] px-3 py-2 text-xs text-[var(--text-secondary)]">
          {report.provider && <span>{t('knowledgeBase.report.provider', { provider: report.provider })}</span>}
          {report.quality && <span className="ml-2">{t('knowledgeBase.report.quality', { quality: report.quality })}</span>}
          {report.warnings?.length ? <div className="mt-1 text-amber-600 dark:text-amber-300">{report.warnings.join(t('knowledgeBase.report.warningSeparator'))}</div> : null}
        </div>
      )}
      {report.asset_bindings?.length ? (
        <div className="mt-3 overflow-hidden rounded-lg border border-[var(--border-color)]">
          <table className="w-full text-xs">
            <thead className="bg-[var(--bg-secondary)] text-left text-[var(--text-secondary)]">
              <tr>
                <th className="px-3 py-2 font-medium">{t('knowledgeBase.report.assetColumns.image')}</th>
                <th className="px-3 py-2 font-medium">{t('knowledgeBase.report.assetColumns.boundSection')}</th>
                <th className="px-3 py-2 font-medium">{t('knowledgeBase.columns.status')}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[var(--border-color)]">
              {report.asset_bindings.map((asset) => (
                <tr key={asset.asset_uri} className="hover:bg-[var(--bg-secondary)]">
                  <td className="px-3 py-2">
                    <div className="max-w-[360px] truncate font-mono text-[11px] text-[var(--text-secondary)]">{asset.asset_uri}</div>
                    {asset.alt_text && <div className="mt-0.5 text-[var(--text-primary)]">{asset.alt_text}</div>}
                  </td>
                  <td className="px-3 py-2">
                    {asset.bound ? (
                      <>
                        <div className="text-[var(--text-primary)]">{asset.node_title || asset.node_id}</div>
                        <div className="mt-0.5 font-mono text-[10px] text-[var(--text-secondary)]">
                          {asset.node_path || '-'} · {asset.node_id} · {t('knowledgeBase.report.line', { line: asset.line || '-' })}{asset.page ? ` · ${t('knowledgeBase.report.page', { page: asset.page })}` : ''}
                        </div>
                      </>
                    ) : (
                      <span className="text-[var(--text-secondary)]">{t('knowledgeBase.report.notInSection')}</span>
                    )}
                  </td>
                  <td className="px-3 py-2">{asset.bound ? <StatusPill tone="green" label={t('knowledgeBase.status.bound')} /> : <StatusPill tone="red" label={t('knowledgeBase.status.unbound')} />}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
      {report.stages?.length ? (
        <div className="mt-3 grid gap-2 md:grid-cols-2">
          {report.stages.map((stage, index) => (
            <div key={`${stage.name}-${index}`} className="rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] px-3 py-2 text-xs">
              <div className="flex items-center justify-between gap-2">
                <span className="font-medium text-[var(--text-primary)]">{stage.name}</span>
                <span className={stage.status === 'ok' ? 'text-emerald-600 dark:text-emerald-300' : 'text-red-600 dark:text-red-300'}>{stage.status}</span>
              </div>
              <div className="mt-1 font-mono text-[11px] text-[var(--text-secondary)]">{stage.duration_ms} ms</div>
              {stage.attributes && Object.keys(stage.attributes).length > 0 && (
                <div className="mt-1 truncate font-mono text-[10px] text-[var(--text-secondary)]">{JSON.stringify(stage.attributes)}</div>
              )}
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function ReportMetric({ label, value, tone = 'normal' }: { label: string; value: string | number; tone?: 'normal' | 'warn' }) {
  return (
    <div className="rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] px-3 py-2">
      <div className="text-[11px] text-[var(--text-secondary)]">{label}</div>
      <div className={`mt-1 font-mono text-sm ${tone === 'warn' ? 'text-amber-600 dark:text-amber-300' : 'text-[var(--text-primary)]'}`}>{value}</div>
    </div>
  );
}

function NamespaceModal(props: { domainId: string; onClose: () => void; onSubmit: (body: { name: string; domain_id: string; index_strategy: string; thinning_enabled: boolean; thinning_token_threshold: number; summary_token_threshold: number; summary_model?: string }) => Promise<void> }) {
  const { t } = useTranslation();
  const [name, setName] = useState('');
  const [thinningEnabled, setThinningEnabled] = useState(true);
  const [thinningThreshold, setThinningThreshold] = useState(1800);
  const [summaryThreshold, setSummaryThreshold] = useState(0);
  const [saving, setSaving] = useState(false);

  const submit = async () => {
    if (!name.trim()) return;
    setSaving(true);
    try {
      await props.onSubmit({
        name: name.trim(),
        domain_id: props.domainId,
        index_strategy: 'markdown_tree',
        thinning_enabled: thinningEnabled,
        thinning_token_threshold: thinningThreshold,
        summary_token_threshold: summaryThreshold,
      });
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal title={t('knowledgeBase.actions.createNamespace')} onClose={props.onClose}>
      <div className="space-y-4">
        <TextField label={t('knowledgeBase.fields.name')} value={name} onChange={setName} autoFocus />
        <NumberField label={t('knowledgeBase.fields.longSectionThreshold')} value={thinningThreshold} onChange={setThinningThreshold} />
        <NumberField label={t('knowledgeBase.fields.summaryThreshold')} value={summaryThreshold} onChange={setSummaryThreshold} />
        <label className="flex items-center gap-2 text-sm text-[var(--text-primary)]">
          <input type="checkbox" checked={thinningEnabled} onChange={(e) => setThinningEnabled(e.target.checked)} />
          {t('knowledgeBase.fields.enableLongSectionProtection')}
        </label>
        <ModalActions onCancel={props.onClose} onConfirm={submit} confirmText={t('knowledgeBase.actions.create')} disabled={!name.trim() || saving} />
      </div>
    </Modal>
  );
}

function IngestModal(props: {
  namespace: KBNamespace;
  domainId: string;
  onPreview: (body: FormData) => Promise<{ title?: string; markdown: string; assets?: KBMarkdownPreviewAsset[]; quality?: string; provider?: string; warnings?: string[] }>;
  onClose: () => void;
  onSubmit: (body: FormData) => Promise<void>;
}) {
  const { t } = useTranslation();
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const assetInputRef = useRef<HTMLInputElement | null>(null);
  const markdownInputRef = useRef<HTMLTextAreaElement | null>(null);
  const [title, setTitle] = useState('');
  const [version, setVersion] = useState('v1');
  const [sourceURI, setSourceURI] = useState('');
  const [markdown, setMarkdown] = useState(() => t('knowledgeBase.ingest.defaultMarkdown'));
  const [markdownMode, setMarkdownMode] = useState<'edit' | 'preview'>('edit');
  const [file, setFile] = useState<File | null>(null);
  const [editableFileName, setEditableFileName] = useState('');
  const [assets, setAssets] = useState<File[]>([]);
  const [convertedAssets, setConvertedAssets] = useState<KBMarkdownPreviewAsset[]>([]);
  const [assetPreviewURLs, setAssetPreviewURLs] = useState<Record<string, string>>({});
  const [previewMeta, setPreviewMeta] = useState<{ provider?: string; quality?: string; warnings?: string[] }>({});
  const [converting, setConverting] = useState(false);
  const [saving, setSaving] = useState(false);

  const previewMarkdown = useMemo(
    () => rewriteMarkdownImageTargetsForPreview(markdown, [...convertedAssets, ...assetsToPreviewAssets(assets, assetPreviewURLs)]),
    [markdown, convertedAssets, assets, assetPreviewURLs],
  );

  useEffect(() => {
    const urls = Object.fromEntries(assets.map((asset) => [assetPreviewKey(asset), URL.createObjectURL(asset)]));
    setAssetPreviewURLs(urls);
    return () => {
      Object.values(urls).forEach((url) => URL.revokeObjectURL(url));
    };
  }, [assets]);

  const handleDocumentFile = async (selected: File | null) => {
    if (!selected) return;
    setPreviewMeta({});
    setConvertedAssets([]);
    if (isEditableMarkdownFile(selected)) {
      const text = await selected.text();
      setMarkdown(text);
      setFile(null);
      setEditableFileName(selected.name);
      setMarkdownMode('edit');
      if (!title.trim()) setTitle(selected.name.replace(/\.[^.]+$/, ''));
      return;
    }
    setFile(selected);
    setEditableFileName('');
    if (!title.trim()) setTitle(selected.name.replace(/\.[^.]+$/, ''));
    setConverting(true);
    try {
      const body = new FormData();
      body.append('file', selected, selected.name);
      const preview = await props.onPreview(body);
      setMarkdown(preview.markdown);
      setConvertedAssets(preview.assets ?? []);
      setPreviewMeta({ provider: preview.provider, quality: preview.quality, warnings: preview.warnings });
      setMarkdownMode('edit');
      if (!title.trim() && preview.title?.trim()) setTitle(preview.title.trim());
    } catch (e: unknown) {
      setMarkdown('');
      setConvertedAssets([]);
      setPreviewMeta({ warnings: [e instanceof Error ? e.message : t('knowledgeBase.errors.previewMarkdown')] });
    } finally {
      setConverting(false);
    }
  };

  const insertAssetReference = (target: string, altText?: string) => {
    const path = target.trim();
    if (!path) return;
    const filename = path.split('/').pop() ?? path;
    const alt = altText?.trim() || filename.replace(/\.[^.]+$/, '') || filename;
    const snippet = `![${alt}](${path})`;
    const input = markdownInputRef.current;
    const start = input?.selectionStart ?? markdown.length;
    const end = input?.selectionEnd ?? markdown.length;
    const before = markdown.slice(0, start);
    const after = markdown.slice(end);
    const prefix = before === '' || before.endsWith('\n') ? '' : '\n';
    const suffix = after === '' || after.startsWith('\n') ? '' : '\n';
    const next = `${before}${prefix}${snippet}${suffix}${after}`;
    const cursor = before.length + prefix.length + snippet.length;
    setMarkdown(next);
    setMarkdownMode('edit');
    requestAnimationFrame(() => {
      markdownInputRef.current?.focus();
      markdownInputRef.current?.setSelectionRange(cursor, cursor);
    });
  };

  const submit = async () => {
    if (!title.trim() || !markdown.trim()) return;
    setSaving(true);
    try {
      const body = new FormData();
      if (title.trim()) body.append('title', title.trim());
      body.append('version', version.trim() || 'v1');
      if (sourceURI.trim()) body.append('source_uri', sourceURI.trim());
      if (file) {
        body.append('file', file, file.name);
      }
      body.append('markdown', markdown);
      for (const asset of assets) {
        body.append('assets', asset, asset.name);
      }
      await props.onSubmit(body);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal title={t('knowledgeBase.ingest.modalTitle', { namespace: props.namespace.name })} onClose={props.onClose} wide>
      <div className="space-y-4">
        <div className="grid gap-3 md:grid-cols-2">
          <TextField label={t('knowledgeBase.fields.title')} value={title} onChange={setTitle} autoFocus />
          <TextField label={t('knowledgeBase.fields.version')} value={version} onChange={setVersion} />
        </div>
        <TextField label={t('knowledgeBase.fields.sourceURI')} value={sourceURI} onChange={setSourceURI} />
        <div className="flex flex-wrap gap-2">
          <input ref={fileInputRef} type="file" accept=".md,.markdown,.txt,.docx,.pdf,text/*,application/pdf,application/vnd.openxmlformats-officedocument.wordprocessingml.document" className="hidden" onChange={(e) => { void handleDocumentFile(e.currentTarget.files?.[0] ?? null); }} />
          <button onClick={() => fileInputRef.current?.click()} className="inline-flex items-center gap-1.5 rounded-lg border border-[var(--border-color)] px-3 py-2 text-sm text-[var(--text-primary)] hover:bg-[var(--bg-secondary)]">
            <UploadCloud className="h-4 w-4" />
            {t('knowledgeBase.actions.selectDocumentFile')}
          </button>
          <input ref={assetInputRef} type="file" accept="image/*" multiple className="hidden" onChange={(e) => handleAssetFiles(e.currentTarget.files, setAssets)} />
          <button onClick={() => assetInputRef.current?.click()} className="inline-flex items-center gap-1.5 rounded-lg border border-[var(--border-color)] px-3 py-2 text-sm text-[var(--text-primary)] hover:bg-[var(--bg-secondary)]">
            <Plus className="h-4 w-4" />
            {t('knowledgeBase.actions.addImageAssets')}
          </button>
        </div>
        {file && (
          <div className="rounded-lg bg-[var(--bg-secondary)] px-3 py-2 text-xs text-[var(--text-secondary)]">
            {t('knowledgeBase.ingest.documentFileHint', { filename: file.name })}
          </div>
        )}
        {converting && (
          <div className="rounded-lg bg-[var(--accent-subtle)] px-3 py-2 text-xs text-[var(--accent-700)] dark:text-[var(--accent-300)]">
            {t('knowledgeBase.ingest.converting')}
          </div>
        )}
        {editableFileName && (
          <div className="rounded-lg bg-[var(--bg-secondary)] px-3 py-2 text-xs text-[var(--text-secondary)]">
            {t('knowledgeBase.ingest.editableFileHint', { filename: editableFileName })}
          </div>
        )}
        {(assets.length > 0 || convertedAssets.length > 0) && (
          <div className="rounded-lg bg-[var(--bg-secondary)] px-3 py-2 text-xs text-[var(--text-secondary)]">
            <div className="mb-2">{t('knowledgeBase.ingest.imageAssets', { assets: [...convertedAssets.map((asset) => asset.path), ...assets.map((asset) => asset.name)].join(', ') })}</div>
            <div className="flex flex-wrap gap-1.5">
              {convertedAssets.map((asset) => (
                <button
                  key={`converted-${asset.path}`}
                  type="button"
                  onClick={() => insertAssetReference(asset.path, asset.alt_text)}
                  className="rounded-md border border-[var(--border-color)] bg-[var(--bg-card)] px-2 py-1 text-[11px] text-[var(--text-primary)] hover:bg-[var(--bg-primary)]"
                >
                  {t('knowledgeBase.actions.insertAsset', { asset: asset.filename || asset.path })}
                </button>
              ))}
              {assets.map((asset) => (
                <button
                  key={`${asset.name}-${asset.size}-${asset.lastModified}`}
                  type="button"
                  onClick={() => insertAssetReference(asset.name)}
                  className="rounded-md border border-[var(--border-color)] bg-[var(--bg-card)] px-2 py-1 text-[11px] text-[var(--text-primary)] hover:bg-[var(--bg-primary)]"
                >
                  {t('knowledgeBase.actions.insertAsset', { asset: asset.name })}
                </button>
              ))}
            </div>
          </div>
        )}
        {(previewMeta.provider || previewMeta.quality || (previewMeta.warnings?.length ?? 0) > 0) && (
          <div className="rounded-lg bg-[var(--bg-secondary)] px-3 py-2 text-xs text-[var(--text-secondary)]">
            {previewMeta.provider && <span>{t('knowledgeBase.report.provider', { provider: previewMeta.provider })}</span>}
            {previewMeta.quality && <span className="ml-2">{t('knowledgeBase.report.quality', { quality: previewMeta.quality })}</span>}
            {previewMeta.warnings?.length ? <div className="mt-1 text-amber-600 dark:text-amber-300">{previewMeta.warnings.join(t('knowledgeBase.report.warningSeparator'))}</div> : null}
          </div>
        )}
        <div className="space-y-2">
          <div className="flex items-center justify-between gap-3">
            <span className="text-xs text-[var(--text-secondary)]">{t('knowledgeBase.ingest.markdown')}</span>
            <div className="inline-flex rounded-lg border border-[var(--border-color)] bg-[var(--bg-secondary)] p-0.5 md:hidden">
              <button
                type="button"
                onClick={() => setMarkdownMode('edit')}
                className={`rounded-md px-2 py-1 text-xs ${markdownMode === 'edit' ? 'bg-[var(--bg-card)] text-[var(--text-primary)] shadow-sm' : 'text-[var(--text-secondary)]'}`}
              >
                {t('knowledgeBase.actions.edit')}
              </button>
              <button
                type="button"
                onClick={() => setMarkdownMode('preview')}
                className={`rounded-md px-2 py-1 text-xs ${markdownMode === 'preview' ? 'bg-[var(--bg-card)] text-[var(--text-primary)] shadow-sm' : 'text-[var(--text-secondary)]'}`}
              >
                {t('knowledgeBase.actions.preview')}
              </button>
            </div>
          </div>
          <div className="grid gap-3 md:grid-cols-2">
            <label className={`${markdownMode === 'preview' ? 'hidden md:block' : 'block'}`}>
              <span className="mb-1 hidden text-[11px] text-[var(--text-secondary)] md:block">{t('knowledgeBase.actions.edit')}</span>
              <textarea ref={markdownInputRef} value={markdown} onChange={(e) => setMarkdown(e.target.value)} rows={16} className="h-[360px] w-full resize-none rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] p-3 font-mono text-sm text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)]" />
            </label>
            <div className={`${markdownMode === 'edit' ? 'hidden md:block' : 'block'}`}>
              <span className="mb-1 hidden text-[11px] text-[var(--text-secondary)] md:block">{t('knowledgeBase.ingest.renderedPreview')}</span>
              <div className="kb-markdown-preview-shell h-[360px] overflow-hidden rounded-lg border border-[var(--border-color)] bg-[var(--bg-secondary)]">
                <div className="h-full overflow-auto p-3">
                  <div className="kb-markdown-preview markdown-prose prose prose-sm mx-auto min-h-full max-w-[760px] rounded-lg border border-[var(--border-color)] bg-[var(--bg-card)] px-4 py-4 text-[13px] leading-[1.65] text-[var(--text-primary)] shadow-sm dark:prose-invert md:px-6 md:py-5">
                    {previewMarkdown.trim() ? (
                      <Streamdown plugins={STREAMDOWN_PREVIEW_PLUGINS} allowedTags={ALLOWED_TAGS}>
                        {previewMarkdown}
                      </Streamdown>
                    ) : (
                      <div className="flex h-full items-center justify-center text-sm text-[var(--text-secondary)]">{t('knowledgeBase.empty.markdown')}</div>
                    )}
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
        <ModalActions onCancel={props.onClose} onConfirm={submit} confirmText={t('knowledgeBase.actions.import')} disabled={saving || converting || !title.trim() || !markdown.trim()} />
      </div>
    </Modal>
  );
}

function BindingModal(props: { namespaces: KBNamespace[]; selectedNamespaceId: string; domainId: string; onClose: () => void; onSubmit: (body: { namespace_id: string; domain_id: string; binding_type: KBBindingType; binding_target: string }) => Promise<void> }) {
  const { t } = useTranslation();
  const [namespaceId, setNamespaceId] = useState(props.selectedNamespaceId || props.namespaces[0]?.id || '');
  const [bindingType, setBindingType] = useState<KBBindingType>('user');
  const [bindingTarget, setBindingTarget] = useState('');
  const [saving, setSaving] = useState(false);

  const submit = async () => {
    if (!namespaceId || !bindingTarget.trim()) return;
    setSaving(true);
    try {
      await props.onSubmit({
        namespace_id: namespaceId,
        domain_id: props.domainId,
        binding_type: bindingType,
        binding_target: bindingTarget.trim(),
      });
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal title={t('knowledgeBase.actions.createBinding')} onClose={props.onClose}>
      <div className="space-y-4">
        <label className="block">
          <span className="mb-1 block text-xs text-[var(--text-secondary)]">{t('knowledgeBase.columns.namespace')}</span>
          <select value={namespaceId} onChange={(e) => setNamespaceId(e.target.value)} className="w-full rounded-lg border border-[var(--border-color)] bg-[var(--bg-card)] px-3 py-2 text-sm text-[var(--text-primary)]">
            {props.namespaces.map((namespace) => <option key={namespace.id} value={namespace.id}>{namespace.name}</option>)}
          </select>
        </label>
        <label className="block">
          <span className="mb-1 block text-xs text-[var(--text-secondary)]">{t('knowledgeBase.fields.bindingType')}</span>
          <select value={bindingType} onChange={(e) => setBindingType(e.target.value as KBBindingType)} className="w-full rounded-lg border border-[var(--border-color)] bg-[var(--bg-card)] px-3 py-2 text-sm text-[var(--text-primary)]">
            {bindingTypes.map((type) => <option key={type} value={type}>{t(`knowledgeBase.bindingTypes.${type}`)}</option>)}
          </select>
        </label>
        <TextField label={t('knowledgeBase.fields.bindingTarget')} value={bindingTarget} onChange={setBindingTarget} autoFocus />
        <ModalActions onCancel={props.onClose} onConfirm={submit} confirmText={t('knowledgeBase.actions.create')} disabled={!namespaceId || !bindingTarget.trim() || saving} />
      </div>
    </Modal>
  );
}

function Modal({ title, children, onClose, wide = false }: { title: string; children: React.ReactNode; onClose: () => void; wide?: boolean }) {
  const { t } = useTranslation();
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div className={`max-h-[90vh] w-full overflow-auto rounded-xl border border-[var(--border-color)] bg-[var(--bg-card)] shadow-2xl ${wide ? 'max-w-5xl' : 'max-w-lg'}`}>
        <div className="flex items-center justify-between border-b border-[var(--border-color)] px-5 py-4">
          <h2 className="text-sm font-semibold text-[var(--text-primary)]">{title}</h2>
          <button onClick={onClose} className="text-sm text-[var(--text-secondary)] hover:text-[var(--text-primary)]">{t('common.close')}</button>
        </div>
        <div className="p-5">{children}</div>
      </div>
    </div>
  );
}

function TextField({ label, value, onChange, autoFocus = false }: { label: string; value: string; onChange: (value: string) => void; autoFocus?: boolean }) {
  return (
    <label className="block">
      <span className="mb-1 block text-xs text-[var(--text-secondary)]">{label}</span>
      <input autoFocus={autoFocus} value={value} onChange={(e) => onChange(e.target.value)} className="w-full rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] px-3 py-2 text-sm text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)]" />
    </label>
  );
}

function NumberField({ label, value, onChange }: { label: string; value: number; onChange: (value: number) => void }) {
  return (
    <label className="block">
      <span className="mb-1 block text-xs text-[var(--text-secondary)]">{label}</span>
      <input type="number" min="0" value={value} onChange={(e) => onChange(Number(e.target.value))} className="w-full rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] px-3 py-2 text-sm text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)]" />
    </label>
  );
}

function ModalActions({ onCancel, onConfirm, confirmText, disabled }: { onCancel: () => void; onConfirm: () => void; confirmText: string; disabled?: boolean }) {
  const { t } = useTranslation();
  return (
    <div className="flex justify-end gap-2 border-t border-[var(--border-color)] pt-4">
      <button onClick={onCancel} className="rounded-lg border border-[var(--border-color)] px-3 py-2 text-sm text-[var(--text-primary)] hover:bg-[var(--bg-secondary)]">{t('common.cancel')}</button>
      <button onClick={onConfirm} disabled={disabled} className="rounded-lg bg-[var(--accent-600)] px-3 py-2 text-sm text-white hover:bg-[var(--accent-700)] disabled:opacity-50">{confirmText}</button>
    </div>
  );
}

function IconButton({ icon: Icon, label, onClick, disabled }: { icon: typeof RefreshCw; label: string; onClick: () => void; disabled?: boolean }) {
  return (
    <button onClick={onClick} disabled={disabled} className="inline-flex items-center gap-1.5 rounded-lg border border-[var(--border-color)] px-3 py-2 text-sm text-[var(--text-primary)] hover:bg-[var(--bg-secondary)] disabled:opacity-50">
      <Icon className="h-4 w-4" />
      {label}
    </button>
  );
}

function EmptyRow({ colSpan, text }: { colSpan: number; text: string }) {
  return (
    <tr>
      <td colSpan={colSpan} className="px-4 py-10 text-center text-sm text-[var(--text-secondary)]">{text}</td>
    </tr>
  );
}

function StatusBadge({ status }: { status: KBDocumentStatus }) {
  const { t } = useTranslation();
  if (status === 'active') return <StatusPill tone="green" label={t('knowledgeBase.documentStatuses.active')} />;
  if (status === 'archived') return <StatusPill tone="gray" label={t('knowledgeBase.documentStatuses.archived')} />;
  if (status === 'revoked') return <StatusPill tone="red" label={t('knowledgeBase.documentStatuses.revoked')} />;
  return <StatusPill tone="blue" label={t(`knowledgeBase.documentStatuses.${status}`)} />;
}

function StatusPill({ tone, label }: { tone: 'green' | 'gray' | 'red' | 'blue'; label: string }) {
  const cls = {
    green: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300',
    gray: 'bg-[var(--bg-secondary)] text-[var(--text-secondary)]',
    red: 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300',
    blue: 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300',
  }[tone];
  return <span className={`inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${cls}`}>{label}</span>;
}

function formatTime(value?: string) {
  if (!value) return '-';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function handleAssetFiles(files: FileList | null | undefined, setAssets: React.Dispatch<React.SetStateAction<File[]>>) {
  if (!files) return;
  setAssets((prev) => [...prev, ...Array.from(files)]);
}

function assetPreviewKey(file: File) {
  return `${file.name}-${file.size}-${file.lastModified}`;
}

function assetsToPreviewAssets(files: File[], urls: Record<string, string>): KBMarkdownPreviewAsset[] {
  return files.map((file) => ({
    path: file.name,
    filename: file.name,
    mime_type: file.type || 'application/octet-stream',
    size: file.size,
    data_url: urls[assetPreviewKey(file)],
  }));
}

function rewriteMarkdownImageTargetsForPreview(markdown: string, assets: KBMarkdownPreviewAsset[]) {
  if (!markdown || assets.length === 0) return markdown;
  const dataURLByPath = new Map<string, string>();
  for (const asset of assets) {
    if (!asset.data_url) continue;
    const paths = [
      asset.path,
      asset.filename,
      asset.path ? asset.path.replace(/^\.\//, '') : '',
      asset.path ? asset.path.split('/').pop() : '',
    ];
    for (const path of paths) {
      if (path) dataURLByPath.set(path, asset.data_url);
    }
  }
  if (dataURLByPath.size === 0) return markdown;
  return markdown.replace(/!\[([^\]]*)\]\(([^)]+)\)/g, (match, alt: string, rawTarget: string) => {
    const target = rawTarget.trim();
    if (target.startsWith('asset://') || target.startsWith('data:') || target.startsWith('http://') || target.startsWith('https://')) {
      return match;
    }
    const dataURL = dataURLByPath.get(target) ?? dataURLByPath.get(target.replace(/^\.\//, '')) ?? dataURLByPath.get(target.split('/').pop() ?? '');
    return dataURL ? `![${alt}](${dataURL})` : match;
  });
}

function isEditableMarkdownFile(file: File) {
  const name = file.name.toLowerCase();
  const type = file.type.toLowerCase();
  return name.endsWith('.md') || name.endsWith('.markdown') || name.endsWith('.txt') || type.startsWith('text/');
}

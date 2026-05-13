import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useNodeClient } from '../../hooks/useNodeClient';
import { useToastStore } from '../../store/toast';
import type { MCPServerConfig, MCPToolsListResponse } from '../../types/api';

/** 带名称的 MCP 服务端条目（前端编辑用） */
interface MCPServerEntry extends MCPServerConfig {
  name: string;
}

/** 纳秒转可读字符串 */
function formatNanosToStr(nanos: number): string {
  const seconds = nanos / 1e9;
  if (seconds >= 60) {
    return `${Math.round(seconds / 60)}m`;
  }
  return `${Math.round(seconds)}s`;
}

export function MCPServersSettings() {
  const { t } = useTranslation();
  const client = useNodeClient();
  const addToast = useToastStore((s) => s.addToast);

  const [mcpTimeout, setMcpTimeout] = useState('30s');
  const [mcpServers, setMcpServers] = useState<MCPServerEntry[]>([]);
  const [envTexts, setEnvTexts] = useState<Record<number, string>>({});
  const [headerTexts, setHeaderTexts] = useState<Record<number, string>>({});
  const [argsTexts, setArgsTexts] = useState<Record<number, string>>({});
  const [toolCatalog, setToolCatalog] = useState<MCPToolsListResponse | null>(null);
  const [toolCatalogError, setToolCatalogError] = useState('');
  const [loading, setLoading] = useState(true);
  const [applying, setApplying] = useState(false);

  const keyValueToText = (values?: Record<string, string>) => {
    if (!values) return '';
    return Object.entries(values).map(([k, v]) => `${k}=${v}`).join('\n');
  };

  const loadConfig = useCallback(async () => {
    setLoading(true);
    try {
      const [cfg, catalog] = await Promise.all([
        client.getRuntimeConfig(),
        client.listMCPTools().catch((e) => {
          setToolCatalogError(e instanceof Error ? e.message : t('runtimeConfig.mcpToolsLoadFailed'));
          return null;
        }),
      ]);
      if (catalog) {
        setToolCatalog(catalog);
        setToolCatalogError('');
      }
      if (cfg.mcp?.timeout) {
        setMcpTimeout(formatNanosToStr(cfg.mcp.timeout));
      }
      if (cfg.mcp?.servers) {
        const entries: MCPServerEntry[] = Object.entries(cfg.mcp.servers).map(([name, srv]) => ({
          name,
          ...srv,
        }));
        setMcpServers(entries);
        const eTexts: Record<number, string> = {};
        const hTexts: Record<number, string> = {};
        const aTexts: Record<number, string> = {};
        entries.forEach((srv, i) => {
          eTexts[i] = keyValueToText(srv.env);
          hTexts[i] = keyValueToText(srv.headers);
          aTexts[i] = (srv.args || []).join(', ');
        });
        setEnvTexts(eTexts);
        setHeaderTexts(hTexts);
        setArgsTexts(aTexts);
      }
    } catch (e) {
      const msg = e instanceof Error ? e.message : t('runtimeConfig.loadFailed');
      addToast('error', msg);
    } finally {
      setLoading(false);
    }
  }, [client, addToast, t]);

  useEffect(() => {
    loadConfig();
  }, [loadConfig]);

  const parseKeyValueText = (text: string): Record<string, string> => {
    const values: Record<string, string> = {};
    for (const line of text.split('\n')) {
      const eq = line.indexOf('=');
      if (eq > 0) {
        values[line.slice(0, eq).trim()] = line.slice(eq + 1);
      }
    }
    return values;
  };

  const buildMcpPayload = () => {
    const servers: Record<string, MCPServerConfig | null> = {};
    mcpServers.forEach((srv, i) => {
      if (!srv.name) return;
      const env = envTexts[i] !== undefined ? parseKeyValueText(envTexts[i]) : srv.env;
      const headers = headerTexts[i] !== undefined ? parseKeyValueText(headerTexts[i]) : srv.headers;
      servers[srv.name] = {
        command: srv.command,
        args: srv.args,
        env,
        transport: srv.transport || 'stdio',
        url: srv.url,
        headers,
        timeout: srv.timeout,
      };
    });
    return { timeout: mcpTimeout, servers };
  };

  const handleApply = async () => {
    setApplying(true);
    try {
      await client.updateRuntimeConfig({
        mcp: buildMcpPayload(),
      });
      await client.reloadMCP();
      const catalog = await client.listMCPTools();
      setToolCatalog(catalog);
      setToolCatalogError('');
      addToast('success', t('runtimeConfig.applySuccess'));
    } catch (e) {
      const msg = e instanceof Error ? e.message : t('runtimeConfig.applyFailed');
      addToast('error', msg);
    } finally {
      setApplying(false);
    }
  };

  const updateMcpServer = (index: number, field: keyof MCPServerEntry, value: string | string[] | Record<string, string>) => {
    setMcpServers((prev) => prev.map((s, i) => (i === index ? { ...s, [field]: value } : s)));
  };

  const updateMcpServerEnvText = (index: number, text: string) => {
    updateMcpServer(index, 'env', parseKeyValueText(text));
  };

  const updateMcpServerHeaderText = (index: number, text: string) => {
    updateMcpServer(index, 'headers', parseKeyValueText(text));
  };

  const deleteMcpServer = (index: number) => {
    setMcpServers((prev) => prev.filter((_, i) => i !== index));
    const reindex = (prev: Record<number, string>) => {
      const next: Record<number, string> = {};
      let j = 0;
      for (let i = 0; i < Object.keys(prev).length + 1; i++) {
        if (i === index) continue;
        if (prev[i] !== undefined) next[j] = prev[i];
        j++;
      }
      return next;
    };
    setEnvTexts(reindex);
    setHeaderTexts(reindex);
    setArgsTexts(reindex);
  };

  const addMcpServer = () => {
    setMcpServers((prev) => [...prev, { name: '', transport: 'stdio', command: '' }]);
  };

  if (loading) {
    return (
      <div className="flex items-center justify-center py-12 text-[var(--text-secondary)]">
        {t('common.loading')}
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <SettingsSection title={t('runtimeConfig.mcpServers')}>
        <div className="p-5 space-y-4">
          <p className="text-xs text-[var(--text-secondary)] mb-3">
            {t('runtimeConfig.mcpServersHint')}
          </p>

          <MCPRuntimeStatus catalog={toolCatalog} error={toolCatalogError} />

          <div className="flex items-center gap-3 mb-4">
            <span className="text-sm text-[var(--text-secondary)] whitespace-nowrap">{t('runtimeConfig.mcpTimeout')}</span>
            <select
              value={mcpTimeout}
              onChange={(e) => setMcpTimeout(e.target.value)}
              className="px-2 py-1 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)] focus:border-[var(--accent)]"
            >
              <option value="10s">10s</option>
              <option value="30s">30s</option>
              <option value="60s">60s</option>
              <option value="120s">120s</option>
            </select>
          </div>

          {mcpServers.map((srv, i) => (
            <div key={i} className="p-3 rounded-lg border border-[var(--border-color)] space-y-2">
              <div className="flex items-center gap-2">
                <input
                  type="text"
                  value={srv.name}
                  onChange={(e) => updateMcpServer(i, 'name', e.target.value)}
                  placeholder={t('runtimeConfig.mcpServerName')}
                  className="flex-1 px-2 py-1.5 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)] focus:border-[var(--accent)]"
                />
                <select
                  value={srv.transport || 'stdio'}
                  onChange={(e) => updateMcpServer(i, 'transport', e.target.value)}
                  className="px-2 py-1.5 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)] focus:border-[var(--accent)]"
                >
                  <option value="stdio">stdio</option>
                  <option value="sse">sse</option>
                  <option value="http">http</option>
                </select>
                <button
                  onClick={() => deleteMcpServer(i)}
                  className="p-1.5 text-[var(--text-secondary)] hover:text-red-500 transition-colors rounded-lg hover:bg-red-50 dark:hover:bg-red-900/20"
                  title={t('runtimeConfig.deleteMcpServer')}
                >
                  <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" />
                  </svg>
                </button>
              </div>
              {(srv.transport === 'stdio' || !srv.transport) && (
                <div className="space-y-2">
                  <div className="grid grid-cols-2 gap-2">
                    <input
                      type="text"
                      value={srv.command || ''}
                      onChange={(e) => updateMcpServer(i, 'command', e.target.value)}
                      placeholder={t('runtimeConfig.mcpCommand') + ' (npx, node...)'}
                      className="px-2 py-1.5 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)] focus:border-[var(--accent)]"
                    />
                    <input
                      type="text"
                      value={argsTexts[i] ?? (srv.args || []).join(', ')}
                      onChange={(e) => setArgsTexts((prev) => ({ ...prev, [i]: e.target.value }))}
                      onBlur={(e) => {
                        const text = e.target.value;
                        setArgsTexts((prev) => ({ ...prev, [i]: text }));
                        updateMcpServer(i, 'args', text.split(',').map((s) => s.trim()).filter(Boolean));
                      }}
                      placeholder={t('runtimeConfig.mcpArgs') + ' (逗号分隔)'}
                      className="px-2 py-1.5 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)] focus:border-[var(--accent)]"
                    />
                  </div>
                  {/* 配置校验：node + -y 是常见误配——-y 是 npx 的 auto-install 参数，node 不认。给一行内联警告而非静默失败。 */}
                  {srv.command === 'node' && (srv.args || []).some((a) => a === '-y' || a === '--yes') && (
                    /* warning semantic */
                    <div className="text-xs px-2 py-1.5 rounded-md bg-[var(--warning)]/10 dark:bg-[var(--warning)]/10 text-[var(--warning)] dark:text-[var(--warning)] border border-[var(--warning)]/30 dark:border-[var(--warning)]/30">
                      ⚠ <code className="font-mono">-y</code> 是 <code className="font-mono">npx</code> 的参数，<code className="font-mono">node</code> 不认。请把 command 改成 <code className="font-mono">npx</code>。
                    </div>
                  )}
                  <div>
                    <p className="text-xs text-[var(--text-secondary)] mb-1">{t('runtimeConfig.mcpEnv')} (KEY=VALUE, {t('runtimeConfig.mcpEnvHint')})</p>
                    <textarea
                      rows={3}
                      value={envTexts[i] ?? keyValueToText(srv.env)}
                      onChange={(e) => {
                        setEnvTexts((prev) => ({ ...prev, [i]: e.target.value }));
                      }}
                      onBlur={(e) => {
                        updateMcpServerEnvText(i, e.target.value);
                      }}
                      placeholder={'WECHAT_APP_ID=wx...\nWECHAT_APP_SECRET=...'}
                      className="w-full px-2 py-1.5 text-sm font-mono rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)] focus:border-[var(--accent)] resize-none"
                    />
                  </div>
                </div>
              )}
              {(srv.transport === 'sse' || srv.transport === 'http') && (
                <div className="space-y-2">
                  <input
                    type="text"
                    value={srv.url || ''}
                    onChange={(e) => updateMcpServer(i, 'url', e.target.value)}
                    placeholder={t('runtimeConfig.mcpUrl') + ' (https://...)'}
                    className="w-full px-2 py-1.5 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)] focus:border-[var(--accent)]"
                  />
                  <div>
                    <p className="text-xs text-[var(--text-secondary)] mb-1">{t('runtimeConfig.mcpHeaders')} (KEY=VALUE, {t('runtimeConfig.mcpHeadersHint')})</p>
                    <textarea
                      rows={3}
                      value={headerTexts[i] ?? keyValueToText(srv.headers)}
                      onChange={(e) => {
                        setHeaderTexts((prev) => ({ ...prev, [i]: e.target.value }));
                      }}
                      onBlur={(e) => {
                        updateMcpServerHeaderText(i, e.target.value);
                      }}
                      placeholder={'Authorization=Bearer sk_mt_...'}
                      className="w-full px-2 py-1.5 text-sm font-mono rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)] focus:border-[var(--accent)] resize-none"
                    />
                  </div>
                </div>
              )}
            </div>
          ))}

          <button
            onClick={addMcpServer}
            className="flex items-center gap-1 text-sm text-[var(--accent-600)] hover:text-[var(--accent-700)] dark:text-[var(--accent-300)] transition-colors mt-2"
          >
            <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M12 4v16m8-8H4" />
            </svg>
            {t('runtimeConfig.addMcpServer')}
          </button>
        </div>
      </SettingsSection>

      <div className="flex gap-3">
        <button
          onClick={handleApply}
          disabled={applying}
          className="flex-1 px-4 py-2.5 text-sm font-medium text-white bg-[var(--accent-600)] hover:bg-[var(--accent-700)] disabled:opacity-50 rounded-xl transition-colors"
        >
          {applying ? t('common.loading') : t('runtimeConfig.apply')}
        </button>
      </div>
    </div>
  );
}

function MCPRuntimeStatus({ catalog, error }: { catalog: MCPToolsListResponse | null; error: string }) {
  const { t } = useTranslation();

  if (error) {
    return (
      <div className="mb-4 rounded-lg border border-[var(--warning)]/30 bg-[var(--warning)]/10 px-3 py-2 text-xs text-[var(--warning)]">
        {t('runtimeConfig.mcpToolsLoadFailed')}: {error}
      </div>
    );
  }

  if (!catalog) {
    return null;
  }

  const servers = catalog.servers || [];
  return (
    <div className="mb-4 rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] px-3 py-3">
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <span className="font-medium text-[var(--text-primary)]">{t('runtimeConfig.mcpRuntimeTools')}</span>
        <span className="rounded-full bg-[var(--accent-subtle)] px-2 py-0.5 text-[var(--accent-600)]">
          {t('runtimeConfig.mcpToolCount', { count: catalog.mcp_count })}
        </span>
        <span className="rounded-full bg-[var(--bg-secondary)] px-2 py-0.5 text-[var(--text-secondary)]">
          {t('runtimeConfig.mcpTotalToolCount', { count: catalog.total })}
        </span>
      </div>

      {servers.length === 0 ? (
        <div className="mt-2 text-xs text-[var(--text-secondary)]">{t('runtimeConfig.mcpNoRuntimeTools')}</div>
      ) : (
        <div className="mt-3 space-y-2">
          {servers.map((server) => {
            const previewTools = (server.tools || []).slice(0, 6);
            const hidden = Math.max(0, server.count - previewTools.length);
            return (
              <div key={server.name} className="rounded-md border border-[var(--border-color)] bg-[var(--bg-card)] px-3 py-2">
                <div className="flex flex-wrap items-center gap-2 text-xs">
                  <span className="font-mono font-medium text-[var(--text-primary)]">{server.name}</span>
                  <span className="text-[var(--text-secondary)]">
                    {t('runtimeConfig.mcpServerRuntimeCounts', {
                      tools: server.count,
                      resources: server.resources || 0,
                      prompts: server.prompts || 0,
                    })}
                  </span>
                </div>
                {previewTools.length > 0 && (
                  <div className="mt-2 flex flex-wrap gap-1.5">
                    {previewTools.map((tool) => (
                      <span
                        key={tool.name}
                        title={`${tool.name}${tool.risk ? ` · ${tool.risk}` : ''}`}
                        className={`rounded px-1.5 py-0.5 text-[11px] ${
                          tool.requires_approval
                            ? 'bg-amber-500/10 text-amber-700 dark:text-amber-300'
                            : tool.trusted
                              ? 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-300'
                              : 'bg-[var(--bg-secondary)] text-[var(--text-secondary)]'
                        }`}
                      >
                        <code>{tool.name}</code>
                      </span>
                    ))}
                    {hidden > 0 && (
                      <span className="rounded bg-[var(--bg-secondary)] px-1.5 py-0.5 text-[11px] text-[var(--text-secondary)]">
                        +{hidden}
                      </span>
                    )}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

function SettingsSection({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="bg-[var(--bg-card)] border border-[var(--border-color)] rounded-2xl overflow-hidden shadow-sm">
      <div className="px-5 py-4 border-b border-[var(--border-color)] flex items-center justify-between">
        <span className="text-sm font-medium text-[var(--text-primary)]">{title}</span>
      </div>
      {children}
    </div>
  );
}

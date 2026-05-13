import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Plus, Trash2, Check, X, ChevronDown, ChevronUp, Star } from 'lucide-react';
import { useNodeClient } from '../../hooks/useNodeClient';
import { useToastStore } from '../../store/toast';
import type { LLMProviderRecord, LLMModelRecord } from '../../types/api';

// 支持的 provider 类型（与后端 provider.go 对齐）
const PROVIDER_TYPES = [
  'openai', 'anthropic', 'deepseek', 'google', 'azure',
  'groq', 'mistral', 'bedrock', 'doubao', 'qwen',
  'qianfan', 'moonshot', 'minimax', 'custom',
];

const SERVICE_TYPES = ['llm', 'image_gen', 'video_gen', 'tts', 'stt', 'embedding'];
const API_FORMATS = ['chat', 'responses'];

// ── Provider 表单 ─────────────────────────────────────────────────────────────

interface ProviderFormData {
  name: string;
  provider_type: string;
  api_key: string;
  base_url: string;
  is_default: boolean;
  enabled: boolean;
  api_format: string;
  service_type: string;
}

const EMPTY_PROVIDER: ProviderFormData = {
  name: '',
  provider_type: 'openai',
  api_key: '',
  base_url: '',
  is_default: false,
  enabled: true,
  api_format: 'chat',
  service_type: 'llm',
};

interface ProviderFormProps {
  initial?: Partial<ProviderFormData>;
  isEdit?: boolean;
  onSubmit: (data: ProviderFormData) => Promise<void>;
  onCancel: () => void;
}

function ProviderForm({ initial, isEdit, onSubmit, onCancel }: ProviderFormProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState<ProviderFormData>({ ...EMPTY_PROVIDER, ...initial });
  const [saving, setSaving] = useState(false);

  const set = (k: keyof ProviderFormData, v: string | boolean) =>
    setForm((f) => ({ ...f, [k]: v }));

  const handleSubmit = async () => {
    if (!form.name.trim()) return;
    setSaving(true);
    try {
      await onSubmit(form);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="p-4 rounded-xl border border-[var(--border-color)] bg-[var(--bg-card)] space-y-3">
      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className="text-xs text-[var(--text-secondary)] mb-1 block">{t('llm.providerName', '名称（唯一标识）')}</label>
          <input
            type="text"
            value={form.name}
            onChange={(e) => set('name', e.target.value)}
            disabled={isEdit}
            placeholder="my-openai"
            className="w-full px-3 py-2 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)] disabled:opacity-50"
          />
        </div>
        <div>
          <label className="text-xs text-[var(--text-secondary)] mb-1 block">{t('llm.providerType', '类型')}</label>
          <select
            value={form.provider_type}
            onChange={(e) => set('provider_type', e.target.value)}
            className="w-full px-3 py-2 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)]"
          >
            {PROVIDER_TYPES.map((pt) => <option key={pt} value={pt}>{pt}</option>)}
          </select>
        </div>
        <div>
          <label className="text-xs text-[var(--text-secondary)] mb-1 block">{t('llm.apiKey', 'API Key')}</label>
          <input
            type="password"
            value={form.api_key}
            onChange={(e) => set('api_key', e.target.value)}
            placeholder={isEdit ? t('llm.apiKeyPlaceholder', '留空保持不变') : 'sk-...'}
            className="w-full px-3 py-2 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)]"
          />
        </div>
        <div>
          <label className="text-xs text-[var(--text-secondary)] mb-1 block">{t('llm.baseUrl', 'Base URL（留空用默认）')}</label>
          <input
            type="text"
            value={form.base_url}
            onChange={(e) => set('base_url', e.target.value)}
            placeholder="https://www.gmini.xyz/v1"
            className="w-full px-3 py-2 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)]"
          />
        </div>
        <div>
          <label className="text-xs text-[var(--text-secondary)] mb-1 block">{t('llm.serviceType', '服务类型')}</label>
          <select
            value={form.service_type}
            onChange={(e) => set('service_type', e.target.value)}
            className="w-full px-3 py-2 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)]"
          >
            {SERVICE_TYPES.map((st) => <option key={st} value={st}>{st}</option>)}
          </select>
        </div>
        <div>
          <label className="text-xs text-[var(--text-secondary)] mb-1 block">{t('llm.apiFormat', 'API 格式')}</label>
          <select
            value={form.api_format}
            onChange={(e) => set('api_format', e.target.value)}
            className="w-full px-3 py-2 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)]"
          >
            {API_FORMATS.map((af) => <option key={af} value={af}>{af}</option>)}
          </select>
        </div>
      </div>
      <div className="flex items-center gap-4">
        <label className="flex items-center gap-2 text-sm text-[var(--text-secondary)] cursor-pointer">
          <input type="checkbox" checked={form.enabled} onChange={(e) => set('enabled', e.target.checked)} className="rounded" />
          {t('admin.enabled', '启用')}
        </label>
        <label className="flex items-center gap-2 text-sm text-[var(--text-secondary)] cursor-pointer">
          <input type="checkbox" checked={form.is_default} onChange={(e) => set('is_default', e.target.checked)} className="rounded" />
          {t('llm.setDefault', '设为默认')}
        </label>
      </div>
      <div className="flex justify-end gap-2 pt-1">
        <button onClick={onCancel} className="px-3 py-1.5 text-xs rounded-lg border border-[var(--border-color)] text-[var(--text-secondary)] hover:bg-[var(--bg-secondary)] transition-colors">
          <X className="w-3.5 h-3.5 inline mr-1" />{t('common.cancel', '取消')}
        </button>
        <button onClick={handleSubmit} disabled={saving || !form.name.trim()} className="px-3 py-1.5 text-xs rounded-lg bg-[var(--accent-500)] hover:bg-[var(--accent-600)] text-white transition-colors disabled:opacity-50">
          <Check className="w-3.5 h-3.5 inline mr-1" />{saving ? t('common.saving', '保存中...') : t('common.save', '保存')}
        </button>
      </div>
    </div>
  );
}

// ── Model 表单 ────────────────────────────────────────────────────────────────

interface ModelFormData {
  name: string;
  provider_name: string;
  model: string;
  base_url: string;
  api_key: string;
  is_default: boolean;
  enabled: boolean;
}

const EMPTY_MODEL: ModelFormData = {
  name: '',
  provider_name: '',
  model: '',
  base_url: '',
  api_key: '',
  is_default: false,
  enabled: true,
};

interface ModelFormProps {
  providers: LLMProviderRecord[];
  initial?: Partial<ModelFormData>;
  onSubmit: (data: ModelFormData) => Promise<void>;
  onCancel: () => void;
}

function ModelForm({ providers, initial, onSubmit, onCancel }: ModelFormProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState<ModelFormData>({ ...EMPTY_MODEL, ...initial });
  const [saving, setSaving] = useState(false);

  const set = (k: keyof ModelFormData, v: string | boolean) =>
    setForm((f) => ({ ...f, [k]: v }));

  const handleSubmit = async () => {
    if (!form.name.trim() || !form.model.trim()) return;
    setSaving(true);
    try {
      await onSubmit(form);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="p-4 rounded-xl border border-[var(--border-color)] bg-[var(--bg-secondary)] space-y-3 ml-4">
      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className="text-xs text-[var(--text-secondary)] mb-1 block">{t('llm.modelName', '名称（唯一标识）')}</label>
          <input
            type="text"
            value={form.name}
            onChange={(e) => set('name', e.target.value)}
            placeholder="gpt-4o-main"
            className="w-full px-3 py-2 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)]"
          />
        </div>
        <div>
          <label className="text-xs text-[var(--text-secondary)] mb-1 block">{t('llm.modelId', '模型 ID（发送给 API）')}</label>
          <input
            type="text"
            value={form.model}
            onChange={(e) => set('model', e.target.value)}
            placeholder="gpt-4o-2024-11-20"
            className="w-full px-3 py-2 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)]"
          />
        </div>
        <div>
          <label className="text-xs text-[var(--text-secondary)] mb-1 block">{t('llm.provider', 'Provider')}</label>
          <select
            value={form.provider_name}
            onChange={(e) => set('provider_name', e.target.value)}
            className="w-full px-3 py-2 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)]"
          >
            <option value="">{t('llm.noProvider', '（不绑定 Provider）')}</option>
            {providers.map((p) => <option key={p.name} value={p.name}>{p.name} ({p.provider_type})</option>)}
          </select>
        </div>
        <div>
          <label className="text-xs text-[var(--text-secondary)] mb-1 block">{t('llm.baseUrlOverride', 'Base URL（覆盖 Provider）')}</label>
          <input
            type="text"
            value={form.base_url}
            onChange={(e) => set('base_url', e.target.value)}
            placeholder={t('llm.optional', '可选')}
            className="w-full px-3 py-2 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)]"
          />
        </div>
        <div className="col-span-2">
          <label className="text-xs text-[var(--text-secondary)] mb-1 block">{t('llm.apiKeyOverride', 'API Key（覆盖 Provider，留空继承）')}</label>
          <input
            type="password"
            value={form.api_key}
            onChange={(e) => set('api_key', e.target.value)}
            placeholder={t('llm.optional', '可选')}
            className="w-full px-3 py-2 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)]"
          />
        </div>
      </div>
      <div className="flex items-center gap-4">
        <label className="flex items-center gap-2 text-sm text-[var(--text-secondary)] cursor-pointer">
          <input type="checkbox" checked={form.enabled} onChange={(e) => set('enabled', e.target.checked)} className="rounded" />
          {t('admin.enabled', '启用')}
        </label>
        <label className="flex items-center gap-2 text-sm text-[var(--text-secondary)] cursor-pointer">
          <input type="checkbox" checked={form.is_default} onChange={(e) => set('is_default', e.target.checked)} className="rounded" />
          {t('llm.setDefault', '设为默认')}
        </label>
      </div>
      <div className="flex justify-end gap-2 pt-1">
        <button onClick={onCancel} className="px-3 py-1.5 text-xs rounded-lg border border-[var(--border-color)] text-[var(--text-secondary)] hover:bg-[var(--bg-secondary)] transition-colors">
          <X className="w-3.5 h-3.5 inline mr-1" />{t('common.cancel', '取消')}
        </button>
        <button onClick={handleSubmit} disabled={saving || !form.name.trim() || !form.model.trim()} className="px-3 py-1.5 text-xs rounded-lg bg-[var(--accent-500)] hover:bg-[var(--accent-600)] text-white transition-colors disabled:opacity-50">
          <Check className="w-3.5 h-3.5 inline mr-1" />{saving ? t('common.saving', '保存中...') : t('common.save', '保存')}
        </button>
      </div>
    </div>
  );
}

// ── 主页面 ────────────────────────────────────────────────────────────────────

export function LLMProviders() {
  const { t } = useTranslation();
  const client = useNodeClient();
  const addToast = useToastStore((s) => s.addToast);

  const [providers, setProviders] = useState<LLMProviderRecord[]>([]);
  const [models, setModels] = useState<LLMModelRecord[]>([]);
  const [loading, setLoading] = useState(true);

  // Provider 状态
  const [creatingProvider, setCreatingProvider] = useState(false);
  const [editingProvider, setEditingProvider] = useState<string | null>(null);
  const [deleteProviderConfirm, setDeleteProviderConfirm] = useState<string | null>(null);
  const [expandedProviders, setExpandedProviders] = useState<Set<string>>(new Set());

  // Model 状态（按 provider 分组）
  const [creatingModelFor, setCreatingModelFor] = useState<string | null>(null);
  const [editingModel, setEditingModel] = useState<string | null>(null);
  const [deleteModelConfirm, setDeleteModelConfirm] = useState<string | null>(null);

  // 独立模型区（无 provider 绑定）
  const [showOrphanModels, setShowOrphanModels] = useState(false);
  const [creatingOrphanModel, setCreatingOrphanModel] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [provRes, modRes] = await Promise.all([
        client.adminListLLMProviders(),
        client.adminListLLMModels(),
      ]);
      setProviders(provRes.providers ?? []);
      setModels(modRes.models ?? []);
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : '加载失败');
    } finally {
      setLoading(false);
    }
  }, [client, addToast]);

  useEffect(() => { load(); }, [load]);

  // ── Provider CRUD ──
  const handleCreateProvider = async (data: ProviderFormData) => {
    await client.adminCreateLLMProvider(data);
    addToast('success', `Provider "${data.name}" 已创建`);
    setCreatingProvider(false);
    load();
  };

  const handleUpdateProvider = async (name: string, data: ProviderFormData) => {
    await client.adminUpdateLLMProvider(name, data);
    addToast('success', `Provider "${name}" 已更新`);
    setEditingProvider(null);
    load();
  };

  const handleDeleteProvider = async () => {
    if (!deleteProviderConfirm) return;
    const name = deleteProviderConfirm;
    setDeleteProviderConfirm(null);
    try {
      await client.adminDeleteLLMProvider(name);
      addToast('success', `Provider "${name}" 已删除`);
      load();
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : '删除失败');
    }
  };

  const toggleProvider = (name: string) =>
    setExpandedProviders((s) => {
      const next = new Set(s);
      if (next.has(name)) {
        next.delete(name);
      } else {
        next.add(name);
      }
      return next;
    });

  // ── Model CRUD ──
  const handleCreateModel = async (data: ModelFormData) => {
    await client.adminCreateLLMModel(data);
    addToast('success', `Model "${data.name}" 已创建`);
    setCreatingModelFor(null);
    setCreatingOrphanModel(false);
    load();
  };

  const handleUpdateModel = async (name: string, data: ModelFormData) => {
    await client.adminUpdateLLMModel(name, data);
    const newName = data.name.trim();
    addToast('success', newName && newName !== name ? `Model "${name}" 已重命名为 "${newName}"` : `Model "${name}" 已更新`);
    setEditingModel(null);
    load();
  };

  const handleDeleteModel = async () => {
    if (!deleteModelConfirm) return;
    const name = deleteModelConfirm;
    setDeleteModelConfirm(null);
    try {
      await client.adminDeleteLLMModel(name);
      addToast('success', `Model "${name}" 已删除`);
      load();
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : '删除失败');
    }
  };

  const modelsForProvider = (provName: string) =>
    models.filter((m) => m.provider_name === provName);

  const orphanModels = models.filter((m) => !m.provider_name);

  return (
    <div className="p-6 max-w-4xl mx-auto">
      <div className="mb-6 flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold text-[var(--text-primary)]">{t('llm.title', 'LLM 管理')}</h1>
          <p className="text-sm text-[var(--text-secondary)] mt-1">
            {t('llm.desc', '管理大模型 Provider 和模型配置')}
          </p>
        </div>
        <button
          onClick={() => { setCreatingProvider(true); setEditingProvider(null); }}
          className="flex items-center gap-2 px-3 py-2 text-sm rounded-lg bg-[var(--accent-500)] hover:bg-[var(--accent-600)] text-white transition-colors"
        >
          <Plus className="w-4 h-4" />
          {t('llm.addProvider', '添加 Provider')}
        </button>
      </div>

      {creatingProvider && (
        <div className="mb-4">
          <ProviderForm
            onSubmit={handleCreateProvider}
            onCancel={() => setCreatingProvider(false)}
          />
        </div>
      )}

      {loading ? (
        <div className="text-center py-12 text-[var(--text-secondary)] text-sm animate-pulse">{t('common.loading', '加载中...')}</div>
      ) : (
        <div className="space-y-3">
          {providers.length === 0 && !creatingProvider && (
            <div className="text-center py-12 text-[var(--text-secondary)] text-sm">
              {t('llm.noProviders', '暂无 Provider，点击右上角添加')}
            </div>
          )}

          {providers.map((p) => {
            const provModels = modelsForProvider(p.name);
            const expanded = expandedProviders.has(p.name);

            return (
              <div key={p.name} className="rounded-xl border border-[var(--border-color)] bg-[var(--bg-card)] overflow-hidden">
                {/* Provider 行 */}
                {editingProvider === p.name ? (
                  <div className="p-4">
                    <ProviderForm
                      initial={{
                        name: p.name,
                        provider_type: p.provider_type,
                        base_url: p.base_url,
                        is_default: p.is_default,
                        enabled: p.enabled,
                        api_format: p.api_format,
                        service_type: p.service_type || 'llm',
                      }}
                      isEdit
                      onSubmit={(data) => handleUpdateProvider(p.name, data)}
                      onCancel={() => setEditingProvider(null)}
                    />
                  </div>
                ) : (
                  <div className="flex items-center justify-between px-4 py-3">
                    <div className="flex items-center gap-3">
                      <button onClick={() => toggleProvider(p.name)} className="text-[var(--text-secondary)] hover:text-[var(--text-primary)] transition-colors">
                        {expanded ? <ChevronUp className="w-4 h-4" /> : <ChevronDown className="w-4 h-4" />}
                      </button>
                      <div>
                        <div className="flex items-center gap-2">
                          <span className="font-medium text-[var(--text-primary)]">{p.name}</span>
                          {p.is_default && (
                            <Star className="w-3.5 h-3.5 text-[var(--accent-500)] fill-[var(--accent-500)]" aria-label={t('llm.default', '默认')} />
                          )}
                          <span className="text-xs px-1.5 py-0.5 rounded bg-[var(--bg-secondary)] text-[var(--text-secondary)]">
                            {p.provider_type}
                          </span>
                          {p.service_type && p.service_type !== 'llm' && (
                            <span className="text-xs px-1.5 py-0.5 rounded bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400">
                              {p.service_type}
                            </span>
                          )}
                        </div>
                        <div className="text-xs text-[var(--text-secondary)] mt-0.5">
                          {p.base_url || t('llm.defaultUrl', '默认 URL')}
                          {p.api_key && <span className="ml-2 font-mono">{p.api_key}</span>}
                        </div>
                      </div>
                    </div>
                    <div className="flex items-center gap-2">
                      <span className={`text-xs px-2 py-0.5 rounded-full font-medium ${
                        p.enabled
                          ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400'
                          : 'bg-[var(--bg-secondary)] text-[var(--text-secondary)]'
                      }`}>
                        {p.enabled ? t('admin.enabled', '已启用') : t('admin.disabled', '已禁用')}
                      </span>
                      <span className="text-xs text-[var(--text-secondary)]">
                        {provModels.length} {t('llm.models', '个模型')}
                      </span>
                      <button
                        onClick={() => setEditingProvider(p.name)}
                        className="text-xs px-2.5 py-1 rounded-lg border border-[var(--border-color)] text-[var(--text-secondary)] hover:bg-[var(--bg-secondary)] transition-colors"
                      >
                        {t('common.edit', '编辑')}
                      </button>
                      <button
                        onClick={() => setDeleteProviderConfirm(p.name)}
                        className="p-1.5 rounded-lg text-[var(--text-secondary)] hover:text-red-500 hover:bg-red-50 dark:hover:bg-red-900/20 transition-colors"
                      >
                        <Trash2 className="w-4 h-4" />
                      </button>
                    </div>
                  </div>
                )}

                {/* 展开：模型列表 */}
                {expanded && editingProvider !== p.name && (
                  <div className="border-t border-[var(--border-color)] bg-[var(--bg-primary)] px-4 py-3 space-y-2">
                    <div className="flex items-center justify-between mb-2">
                      <span className="text-xs font-medium text-[var(--text-secondary)] uppercase tracking-wide">
                        {t('llm.models', '模型')}
                      </span>
                      <button
                        onClick={() => { setCreatingModelFor(p.name); setEditingModel(null); }}
                        className="flex items-center gap-1 text-xs px-2 py-1 rounded-lg border border-[var(--border-color)] text-[var(--accent-600)] hover:bg-[var(--accent-50)] dark:hover:bg-[var(--accent-light)] transition-colors"
                      >
                        <Plus className="w-3 h-3" /> {t('llm.addModel', '添加模型')}
                      </button>
                    </div>

                    {creatingModelFor === p.name && (
                      <ModelForm
                        providers={providers}
                        initial={{ provider_name: p.name }}
                        onSubmit={handleCreateModel}
                        onCancel={() => setCreatingModelFor(null)}
                      />
                    )}

                    {provModels.length === 0 && creatingModelFor !== p.name && (
                      <div className="text-xs text-[var(--text-secondary)] py-2">
                        {t('llm.noModels', '暂无模型，点击"添加模型"')}
                      </div>
                    )}

                    {provModels.map((m) => (
                      <div key={m.name}>
                        {editingModel === m.name ? (
                          <ModelForm
                            providers={providers}
                            initial={{
                              name: m.name,
                              provider_name: m.provider_name,
                              model: m.model,
                              base_url: m.base_url,
                              is_default: m.is_default,
                              enabled: m.enabled,
                            }}
                            onSubmit={(data) => handleUpdateModel(m.name, data)}
                            onCancel={() => setEditingModel(null)}
                          />
                        ) : (
                          <ModelRow
                            model={m}
                            onEdit={() => setEditingModel(m.name)}
                            onDelete={() => setDeleteModelConfirm(m.name)}
                            t={t}
                          />
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            );
          })}

          {/* 独立模型（无 Provider 绑定）*/}
          <div className="rounded-xl border border-dashed border-[var(--border-color)] bg-[var(--bg-card)] overflow-hidden">
            <div className="flex items-center justify-between px-4 py-3">
              <button onClick={() => setShowOrphanModels((v) => !v)} className="flex items-center gap-2 text-[var(--text-secondary)]">
                {showOrphanModels ? <ChevronUp className="w-4 h-4" /> : <ChevronDown className="w-4 h-4" />}
                <span className="text-sm font-medium">{t('llm.standaloneModels', '独立模型')}</span>
                <span className="text-xs text-[var(--text-secondary)]">({orphanModels.length})</span>
              </button>
              <button
                onClick={() => { setCreatingOrphanModel(true); setShowOrphanModels(true); setEditingModel(null); }}
                className="flex items-center gap-1 text-xs px-2 py-1 rounded-lg border border-[var(--border-color)] text-[var(--accent-600)] hover:bg-[var(--accent-50)] dark:hover:bg-[var(--accent-light)] transition-colors"
              >
                <Plus className="w-3 h-3" /> {t('llm.addModel', '添加模型')}
              </button>
            </div>

            {showOrphanModels && (
              <div className="border-t border-[var(--border-color)] bg-[var(--bg-primary)] px-4 py-3 space-y-2">
                {creatingOrphanModel && (
                  <ModelForm
                    providers={providers}
                    onSubmit={handleCreateModel}
                    onCancel={() => setCreatingOrphanModel(false)}
                  />
                )}
                {orphanModels.length === 0 && !creatingOrphanModel && (
                  <div className="text-xs text-[var(--text-secondary)] py-2">
                    {t('llm.noStandaloneModels', '暂无独立模型')}
                  </div>
                )}
                {orphanModels.map((m) => (
                  <div key={m.name}>
                    {editingModel === m.name ? (
                      <ModelForm
                        providers={providers}
                        initial={{
                          name: m.name,
                          provider_name: m.provider_name,
                          model: m.model,
                          base_url: m.base_url,
                          is_default: m.is_default,
                          enabled: m.enabled,
                        }}
                        onSubmit={(data) => handleUpdateModel(m.name, data)}
                        onCancel={() => setEditingModel(null)}
                      />
                    ) : (
                      <ModelRow
                        model={m}
                        onEdit={() => setEditingModel(m.name)}
                        onDelete={() => setDeleteModelConfirm(m.name)}
                        t={t}
                      />
                    )}
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      )}

      {/* Provider 删除确认 */}
      {deleteProviderConfirm && (
        <ConfirmDialog
          title={t('llm.deleteProvider', '删除 Provider')}
          message={`确定删除 Provider "${deleteProviderConfirm}"？关联的模型配置不会被自动删除。`}
          onConfirm={handleDeleteProvider}
          onCancel={() => setDeleteProviderConfirm(null)}
        />
      )}

      {/* Model 删除确认 */}
      {deleteModelConfirm && (
        <ConfirmDialog
          title={t('llm.deleteModel', '删除模型')}
          message={`确定删除模型 "${deleteModelConfirm}"？`}
          onConfirm={handleDeleteModel}
          onCancel={() => setDeleteModelConfirm(null)}
        />
      )}
    </div>
  );
}

// ── 共用子组件 ────────────────────────────────────────────────────────────────

function ModelRow({
  model,
  onEdit,
  onDelete,
  t,
}: {
  model: LLMModelRecord;
  onEdit: () => void;
  onDelete: () => void;
  t: TFunction;
}) {
  return (
    <div className="flex items-center justify-between px-3 py-2 rounded-lg bg-[var(--bg-card)] border border-[var(--border-color)]">
      <div>
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium text-[var(--text-primary)]">{model.name}</span>
          {model.is_default && (
            <Star className="w-3 h-3 text-[var(--accent-500)] fill-[var(--accent-500)]" />
          )}
          <span className="text-xs text-[var(--text-secondary)] font-mono">{model.model}</span>
        </div>
        {(model.base_url || model.api_key) && (
          <div className="text-xs text-[var(--text-secondary)] mt-0.5">
            {model.base_url && <span className="mr-2">{model.base_url}</span>}
            {model.api_key && <span className="font-mono">{model.api_key}</span>}
          </div>
        )}
      </div>
      <div className="flex items-center gap-2">
        <span className={`text-xs px-1.5 py-0.5 rounded-full font-medium ${
          model.enabled
            ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400'
            : 'bg-[var(--bg-secondary)] text-[var(--text-secondary)]'
        }`}>
          {model.enabled ? t('admin.enabled', '已启用') : t('admin.disabled', '已禁用')}
        </span>
        <button onClick={onEdit} className="text-xs px-2 py-1 rounded-lg border border-[var(--border-color)] text-[var(--text-secondary)] hover:bg-[var(--bg-secondary)] transition-colors">
          {t('common.edit', '编辑')}
        </button>
        <button onClick={onDelete} className="p-1.5 rounded-lg text-[var(--text-secondary)] hover:text-red-500 hover:bg-red-50 dark:hover:bg-red-900/20 transition-colors">
          <Trash2 className="w-3.5 h-3.5" />
        </button>
      </div>
    </div>
  );
}

function ConfirmDialog({
  title,
  message,
  onConfirm,
  onCancel,
}: {
  title: string;
  message: string;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 backdrop-blur-sm">
      <div className="w-80 rounded-xl bg-[var(--bg-card)] border border-[var(--border-color)] shadow-2xl p-6">
        <h3 className="text-sm font-semibold text-[var(--text-primary)] mb-2">{title}</h3>
        <p className="text-sm text-[var(--text-secondary)] mb-5">{message}</p>
        <div className="flex justify-end gap-2">
          <button onClick={onCancel} className="px-3 py-1.5 text-xs rounded-lg border border-[var(--border-color)] text-[var(--text-secondary)] hover:bg-[var(--bg-secondary)] transition-colors">
            取消
          </button>
          <button onClick={onConfirm} className="px-3 py-1.5 text-xs rounded-lg bg-red-600 hover:bg-red-700 text-white transition-colors">
            删除
          </button>
        </div>
      </div>
    </div>
  );
}

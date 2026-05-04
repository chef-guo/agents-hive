import { useEffect, useState, useCallback } from 'react';
import { AlertTriangle, FileText, Edit2, Trash2, RotateCcw, Save, X, ChevronDown, ChevronRight } from 'lucide-react';
import { useNodeClient } from '../../hooks/useNodeClient';
import { useToastStore } from '../../store/toast';
import type { PromptRecord } from '../../types/api';

// 已知的 prompt key 分组（与 go:embed 目录对应）
const KNOWN_KEYS = [
  { group: 'system', keys: ['system/base', 'system/execution', 'system/plan_runtime', 'system/business', 'system/code_editing', 'system/safety', 'system/reply'] },
  { group: 'tools', keys: ['tools/wenyan', 'tools/spawn_agent', 'tools/dynamic_tools'] },
  { group: 'subagents', keys: ['subagents/title', 'subagents/summary', 'subagents/compaction', 'subagents/explore', 'subagents/codereview'] },
];

interface EditState {
  key: string;
  language: string;
  content: string;
  isNew: boolean;
}

export function PromptManager() {
  const client = useNodeClient();
  const addToast = useToastStore((s) => s.addToast);

  const [records, setRecords] = useState<PromptRecord[]>([]);
  const [loading, setLoading] = useState(true);
  const [editState, setEditState] = useState<EditState | null>(null);
  const [saving, setSaving] = useState(false);
  const [smokeWarnings, setSmokeWarnings] = useState<string[]>([]);
  const [deleteConfirm, setDeleteConfirm] = useState<{ key: string; language: string } | null>(null);
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(new Set(['system', 'subagents']));

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await client.adminListPrompts(1, 200);
      setRecords(res.items || []);
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : '加载 Prompt 列表失败');
    } finally {
      setLoading(false);
    }
  }, [client, addToast]);

  useEffect(() => { load(); }, [load]);

  const getRecord = (key: string) => records.find((r) => r.key === key);

  const handleEdit = async (key: string) => {
    setSmokeWarnings([]);
    const existing = getRecord(key);
    if (existing) {
      setEditState({ key, language: existing.language, content: existing.content, isNew: false });
      return;
    }
    // 新建：先从 embed 加载默认内容作为初始值
    try {
      const res = await client.adminGetPrompt(key, '');
      setEditState({ key, language: '', content: res.content, isNew: true });
    } catch {
      setEditState({ key, language: '', content: '', isNew: true });
    }
  };

  const handleSave = async () => {
    if (!editState) return;
    if (!editState.content.trim()) { addToast('error', '内容不能为空'); return; }
    setSaving(true);
    setSmokeWarnings([]);
    let closeAfterSave = false;
    try {
      const smoke = await client.adminPromptSmokeEval({
        key: editState.key,
        language: editState.language,
        content: editState.content,
      });
      const warnings = smoke.warnings || [];
      if (!smoke.ok) {
        addToast('error', `Smoke eval 未通过（检查 ${smoke.checked_cases} 个用例）`);
        setSmokeWarnings(warnings);
        return;
      }
      if (warnings.length) {
        setSmokeWarnings(warnings);
        addToast('warning', `Smoke eval 有 ${warnings.length} 条警告，已继续保存`);
      }
      await client.adminUpsertPrompt(editState.key, editState.language, editState.content);
      addToast('success', `Prompt "${editState.key}" 已保存`);
      closeAfterSave = warnings.length === 0;
      load();
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : '保存失败');
      return;
    } finally {
      setSaving(false);
    }
    if (closeAfterSave) setEditState(null);
  };

  const handleDelete = async () => {
    if (!deleteConfirm) return;
    try {
      await client.adminDeletePrompt(deleteConfirm.key, deleteConfirm.language);
      addToast('success', `Prompt "${deleteConfirm.key}" 已恢复为默认值`);
      setDeleteConfirm(null);
      load();
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : '删除失败');
    }
  };

  const toggleGroup = (group: string) => {
    setExpandedGroups((prev) => {
      const next = new Set(prev);
      if (next.has(group)) next.delete(group);
      else next.add(group);
      return next;
    });
  };

  if (loading) {
    return (
      <div className="flex items-center justify-center h-64 text-[var(--text-secondary)]">
        <div className="w-5 h-5 border-2 border-current border-t-transparent rounded-full animate-spin mr-2" />
        加载中...
      </div>
    );
  }

  return (
    <div className="p-6 max-w-5xl mx-auto">
      <div className="mb-6">
        <h1 className="text-xl font-semibold text-[var(--text-primary)] font-display">Prompt 管理</h1>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">
          覆盖内置 Prompt。空表示使用 go:embed 默认值，修改后下一个对话 turn 生效（最多 30 秒延迟）。
        </p>
      </div>

      {/* Prompt 列表 */}
      <div className="space-y-3">
        {KNOWN_KEYS.map(({ group, keys }) => {
          const expanded = expandedGroups.has(group);
          const overrideCount = keys.filter((k) => getRecord(k)).length;
          return (
            <div key={group} className="border border-[var(--border-color)] rounded-xl overflow-hidden">
              {/* Group header */}
              <button
                onClick={() => toggleGroup(group)}
                className="w-full flex items-center justify-between px-4 py-3 bg-[var(--bg-secondary)] hover:bg-[var(--bg-tertiary)] transition-colors text-left"
              >
                <div className="flex items-center gap-2">
                  {expanded ? <ChevronDown className="w-4 h-4 text-[var(--text-secondary)]" /> : <ChevronRight className="w-4 h-4 text-[var(--text-secondary)]" />}
                  <span className="font-medium text-sm text-[var(--text-primary)] capitalize">{group}</span>
                  {overrideCount > 0 && (
                    <span className="px-1.5 py-0.5 text-[10px] font-medium rounded-full bg-[var(--accent-100)] text-[var(--accent-700)] dark:bg-[var(--accent-light)] dark:text-[var(--accent-300)]">
                      {overrideCount} 已覆盖
                    </span>
                  )}
                </div>
                <span className="text-xs text-[var(--text-secondary)]">{keys.length} 个 prompt</span>
              </button>

              {/* Keys */}
              {expanded && (
                <div className="divide-y divide-[var(--border-color)]">
                  {keys.map((key) => {
                    const record = getRecord(key);
                    const shortKey = key.split('/')[1];
                    return (
                      <div key={key} className="flex items-center gap-3 px-4 py-3 hover:bg-[var(--bg-secondary)] transition-colors">
                        <FileText className="w-4 h-4 text-[var(--text-secondary)] shrink-0" />
                        <div className="flex-1 min-w-0">
                          <div className="flex items-center gap-2">
                            <span className="text-sm font-mono text-[var(--text-primary)]">{shortKey}</span>
                            {record ? (
                              <span className="px-1.5 py-0.5 text-[10px] rounded-full bg-emerald-100 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-400">
                                DB 覆盖
                              </span>
                            ) : (
                              <span className="px-1.5 py-0.5 text-[10px] rounded-full bg-[var(--bg-tertiary)] text-[var(--text-secondary)]">
                                默认值
                              </span>
                            )}
                          </div>
                          {record && (
                            <p className="text-xs text-[var(--text-secondary)] mt-0.5 truncate">
                              {record.content.slice(0, 80)}...
                              <span className="ml-2 text-[var(--text-tertiary)]">by {record.updated_by}</span>
                            </p>
                          )}
                        </div>
                        <div className="flex items-center gap-1 shrink-0">
                          <button
                            onClick={() => handleEdit(key)}
                            className="p-1.5 rounded-lg hover:bg-[var(--bg-tertiary)] text-[var(--text-secondary)] hover:text-[var(--text-primary)] transition-colors"
                            title="编辑"
                          >
                            <Edit2 className="w-3.5 h-3.5" />
                          </button>
                          {record && (
                            <button
                              onClick={() => setDeleteConfirm({ key: record.key, language: record.language })}
                              className="p-1.5 rounded-lg hover:bg-red-50 dark:hover:bg-red-900/20 text-[var(--text-secondary)] hover:text-red-500 transition-colors"
                              title="恢复默认值"
                            >
                              <RotateCcw className="w-3.5 h-3.5" />
                            </button>
                          )}
                        </div>
                      </div>
                    );
                  })}
                </div>
              )}
            </div>
          );
        })}
      </div>

      {/* 编辑 Modal */}
      {editState && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 backdrop-blur-sm p-4">
          <div className="bg-[var(--bg-primary)] border border-[var(--border-color)] rounded-2xl shadow-2xl w-full max-w-3xl flex flex-col max-h-[90vh]">
            {/* Header */}
            <div className="flex items-center justify-between px-5 py-4 border-b border-[var(--border-color)]">
              <div>
                <h2 className="font-semibold text-[var(--text-primary)] font-display">编辑 Prompt</h2>
                <p className="text-xs text-[var(--text-secondary)] mt-0.5 font-mono">{editState.key}</p>
              </div>
              <button onClick={() => setEditState(null)} className="p-1.5 rounded-lg hover:bg-[var(--bg-secondary)] text-[var(--text-secondary)]">
                <X className="w-4 h-4" />
              </button>
            </div>

            {/* Language */}
            <div className="px-5 py-3 border-b border-[var(--border-color)] flex items-center gap-3">
              <label className="text-xs text-[var(--text-secondary)] w-16 shrink-0">语言</label>
              <input
                type="text"
                value={editState.language}
                onChange={(e) => {
                  setSmokeWarnings([]);
                  setEditState((s) => s ? { ...s, language: e.target.value } : s);
                }}
                placeholder="留空表示通用（所有语言）"
                className="flex-1 text-sm px-3 py-1.5 rounded-lg border border-[var(--border-color)] bg-[var(--bg-secondary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-500)]"
              />
              <span className="text-xs text-[var(--text-secondary)]">如 zh-CN / en-US</span>
            </div>

            {/* Editor */}
            <div className="flex-1 overflow-hidden px-5 py-3">
              <textarea
                value={editState.content}
                onChange={(e) => {
                  setSmokeWarnings([]);
                  setEditState((s) => s ? { ...s, content: e.target.value } : s);
                }}
                className="w-full h-full min-h-[300px] text-sm font-mono px-3 py-2 rounded-lg border border-[var(--border-color)] bg-[var(--bg-secondary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-500)] resize-none"
                spellCheck={false}
              />
            </div>

            {smokeWarnings.length > 0 && (
              <div className="mx-5 mb-3 rounded-lg border border-[var(--warning)]/30 bg-[var(--warning)]/10 px-3 py-2 text-sm text-[var(--warning)]">
                <div className="flex items-center gap-2 font-medium">
                  <AlertTriangle className="w-4 h-4 shrink-0" />
                  Smoke eval 警告
                </div>
                <ul className="mt-1 space-y-1 pl-6 list-disc text-xs">
                  {smokeWarnings.map((warning) => (
                    <li key={warning}>{warning}</li>
                  ))}
                </ul>
              </div>
            )}

            {/* Footer */}
            <div className="flex items-center justify-between px-5 py-4 border-t border-[var(--border-color)]">
              <p className="text-xs text-[var(--text-secondary)]">
                {editState.isNew ? '将创建新的 DB 覆盖' : '将更新现有覆盖'}，下一个 turn 生效
              </p>
              <div className="flex gap-2">
                <button
                  onClick={() => setEditState(null)}
                  className="px-4 py-2 text-sm rounded-lg border border-[var(--border-color)] text-[var(--text-secondary)] hover:bg-[var(--bg-secondary)] transition-colors"
                >
                  取消
                </button>
                <button
                  onClick={handleSave}
                  disabled={saving}
                  className="px-4 py-2 text-sm rounded-lg bg-[var(--accent-500)] text-white hover:bg-[var(--accent-600)] disabled:opacity-50 transition-colors flex items-center gap-1.5"
                >
                  {saving ? <div className="w-3.5 h-3.5 border-2 border-white border-t-transparent rounded-full animate-spin" /> : <Save className="w-3.5 h-3.5" />}
                  保存
                </button>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* 删除确认 Modal */}
      {deleteConfirm && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 backdrop-blur-sm p-4">
          <div className="bg-[var(--bg-primary)] border border-[var(--border-color)] rounded-2xl shadow-2xl w-full max-w-sm p-6">
            <div className="flex items-center gap-3 mb-4">
              <div className="w-10 h-10 rounded-full bg-[var(--accent-100)] dark:bg-[var(--accent-light)] flex items-center justify-center">
                <RotateCcw className="w-5 h-5 text-[var(--accent-600)] dark:text-[var(--accent-300)]" />
              </div>
              <div>
                <h3 className="font-semibold text-[var(--text-primary)]">恢复默认值</h3>
                <p className="text-xs text-[var(--text-secondary)] mt-0.5 font-mono">{deleteConfirm.key}</p>
              </div>
            </div>
            <p className="text-sm text-[var(--text-secondary)] mb-5">
              将删除 DB 覆盖，恢复为 go:embed 内置默认值。此操作不可撤销。
            </p>
            <div className="flex gap-2 justify-end">
              <button
                onClick={() => setDeleteConfirm(null)}
                className="px-4 py-2 text-sm rounded-lg border border-[var(--border-color)] text-[var(--text-secondary)] hover:bg-[var(--bg-secondary)] transition-colors"
              >
                取消
              </button>
              <button
                onClick={handleDelete}
                className="px-4 py-2 text-sm rounded-lg bg-[var(--accent-500)] text-white hover:bg-[var(--accent-600)] transition-colors flex items-center gap-1.5"
              >
                <Trash2 className="w-3.5 h-3.5" />
                恢复默认
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

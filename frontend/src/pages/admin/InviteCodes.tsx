import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus, Trash2, Copy, Ban, Check } from 'lucide-react';
import { useNodeClient } from '../../hooks/useNodeClient';
import { useToastStore } from '../../store/toast';
import type { AdminInviteCode } from '../../types/api';

function defaultExpiresLocal(): string {
  const d = new Date();
  d.setDate(d.getDate() + 30);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function localInputToRFC3339(value: string): string {
  return new Date(value).toISOString();
}

function formatTime(iso: string): string {
  try {
    return new Date(iso).toLocaleString();
  } catch {
    return iso;
  }
}

function inviteStatus(inv: AdminInviteCode): 'active' | 'disabled' | 'expired' | 'exhausted' {
  if (inv.disabled) return 'disabled';
  if (new Date(inv.expires_at).getTime() <= Date.now()) return 'expired';
  if (inv.use_count >= inv.max_uses) return 'exhausted';
  return 'active';
}

export function InviteCodes() {
  const { t } = useTranslation();
  const client = useNodeClient();
  const addToast = useToastStore((s) => s.addToast);

  const [codes, setCodes] = useState<AdminInviteCode[]>([]);
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [role, setRole] = useState<'user' | 'admin'>('user');
  const [maxUses, setMaxUses] = useState('1');
  const [expiresAt, setExpiresAt] = useState(defaultExpiresLocal);
  const [note, setNote] = useState('');
  const [createdPlain, setCreatedPlain] = useState<{ code: string; invite: AdminInviteCode } | null>(null);
  const [deleteConfirm, setDeleteConfirm] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const list = await client.adminListInviteCodes();
      setCodes(list);
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : t('inviteCodes.loadFailed', '加载邀请码失败'));
    } finally {
      setLoading(false);
    }
  }, [client, addToast, t]);

  useEffect(() => { void load(); }, [load]);

  const handleCreate = async () => {
    const max = parseInt(maxUses, 10);
    if (!expiresAt) {
      addToast('error', t('inviteCodes.expiresRequired', '请设置过期时间'));
      return;
    }
    if (isNaN(max) || max < 1) {
      addToast('error', t('inviteCodes.maxUsesInvalid', '最大使用次数须为正整数'));
      return;
    }
    try {
      const res = await client.adminCreateInviteCode({
        role,
        max_uses: max,
        expires_at: localInputToRFC3339(expiresAt),
        note: note.trim() || undefined,
      });
      setCreatedPlain({ code: res.code, invite: res.invite });
      setCreating(false);
      setNote('');
      setExpiresAt(defaultExpiresLocal());
      setMaxUses('1');
      setRole('user');
      addToast('success', t('inviteCodes.created', '邀请码已创建'));
      load();
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : t('inviteCodes.createFailed', '创建邀请码失败'));
    }
  };

  const handleToggleDisabled = async (inv: AdminInviteCode) => {
    try {
      await client.adminUpdateInviteCode(inv.id, { disabled: !inv.disabled });
      addToast('success', inv.disabled
        ? t('inviteCodes.enabled', '邀请码已启用')
        : t('inviteCodes.disabled', '邀请码已禁用'));
      load();
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : t('inviteCodes.updateFailed', '更新邀请码失败'));
    }
  };

  const handleDeleteConfirmed = async () => {
    if (!deleteConfirm) return;
    const id = deleteConfirm;
    setDeleteConfirm(null);
    try {
      await client.adminDeleteInviteCode(id);
      addToast('success', t('inviteCodes.deleted', '邀请码已删除'));
      load();
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : t('inviteCodes.deleteFailed', '删除邀请码失败'));
    }
  };

  const copyRegisterLink = async (plain: string) => {
    const url = `${window.location.origin}/register/invite?code=${encodeURIComponent(plain)}`;
    try {
      await navigator.clipboard.writeText(url);
      addToast('success', t('inviteCodes.linkCopied', '注册链接已复制'));
    } catch {
      addToast('error', t('inviteCodes.copyFailed', '复制失败，请手动复制'));
    }
  };

  const statusLabel = (status: ReturnType<typeof inviteStatus>) => {
    switch (status) {
      case 'active': return t('inviteCodes.statusActive', '可用');
      case 'disabled': return t('inviteCodes.statusDisabled', '已禁用');
      case 'expired': return t('inviteCodes.statusExpired', '已过期');
      case 'exhausted': return t('inviteCodes.statusExhausted', '已用尽');
    }
  };

  return (
    <div className="p-6 max-w-5xl mx-auto">
      <div className="mb-6 flex items-center justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold text-[var(--text-primary)]">{t('inviteCodes.title', '邀请码')}</h1>
          <p className="text-sm text-[var(--text-secondary)] mt-1">{t('inviteCodes.desc', '创建与管理注册邀请码；明文仅创建时展示一次')}</p>
        </div>
        <button
          type="button"
          onClick={() => setCreating(true)}
          className="flex items-center gap-2 px-3 py-2 text-sm rounded-lg bg-[var(--accent-500)] hover:bg-[var(--accent-600)] text-white transition-colors shrink-0"
        >
          <Plus className="w-4 h-4" />
          {t('inviteCodes.create', '创建邀请码')}
        </button>
      </div>

      {creating && (
        <div className="mb-4 p-4 rounded-xl border border-[var(--border-color)] bg-[var(--bg-card)]">
          <h3 className="text-sm font-medium text-[var(--text-primary)] mb-3">{t('inviteCodes.newTitle', '新建邀请码')}</h3>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <label className="text-xs text-[var(--text-secondary)]">
              {t('inviteCodes.role', '注册角色')}
              <select
                value={role}
                onChange={(e) => setRole(e.target.value as 'user' | 'admin')}
                className="mt-1 w-full px-3 py-2 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)]"
              >
                <option value="user">user</option>
                <option value="admin">admin</option>
              </select>
            </label>
            <label className="text-xs text-[var(--text-secondary)]">
              {t('inviteCodes.maxUses', '最大使用次数')}
              <input
                type="number"
                min={1}
                value={maxUses}
                onChange={(e) => setMaxUses(e.target.value)}
                className="mt-1 w-full px-3 py-2 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)]"
              />
            </label>
            <label className="text-xs text-[var(--text-secondary)] sm:col-span-2">
              {t('inviteCodes.expiresAt', '过期时间')}
              <input
                type="datetime-local"
                value={expiresAt}
                onChange={(e) => setExpiresAt(e.target.value)}
                className="mt-1 w-full px-3 py-2 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)]"
              />
            </label>
            <label className="text-xs text-[var(--text-secondary)] sm:col-span-2">
              {t('inviteCodes.note', '备注（可选）')}
              <input
                type="text"
                value={note}
                onChange={(e) => setNote(e.target.value)}
                className="mt-1 w-full px-3 py-2 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)]"
                placeholder={t('inviteCodes.notePlaceholder', '用途说明')}
              />
            </label>
          </div>
          <div className="flex justify-end gap-2 mt-4">
            <button
              type="button"
              onClick={() => setCreating(false)}
              className="px-3 py-1.5 text-xs rounded-lg border border-[var(--border-color)] text-[var(--text-secondary)]"
            >
              {t('common.cancel', '取消')}
            </button>
            <button
              type="button"
              onClick={() => void handleCreate()}
              className="px-3 py-1.5 text-xs rounded-lg bg-[var(--accent-600)] text-white"
            >
              {t('common.save', '保存')}
            </button>
          </div>
        </div>
      )}

      <div className="rounded-xl border border-[var(--border-color)] overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-[var(--bg-secondary)]">
            <tr>
              <th className="px-4 py-3 text-left font-medium text-[var(--text-secondary)]">{t('inviteCodes.hint', '码尾')}</th>
              <th className="px-4 py-3 text-left font-medium text-[var(--text-secondary)]">{t('inviteCodes.role', '角色')}</th>
              <th className="px-4 py-3 text-left font-medium text-[var(--text-secondary)]">{t('inviteCodes.usage', '用量')}</th>
              <th className="px-4 py-3 text-left font-medium text-[var(--text-secondary)]">{t('inviteCodes.expiresAt', '过期')}</th>
              <th className="px-4 py-3 text-left font-medium text-[var(--text-secondary)]">{t('admin.status', '状态')}</th>
              <th className="px-4 py-3 text-left font-medium text-[var(--text-secondary)]">{t('inviteCodes.note', '备注')}</th>
              <th className="px-4 py-3 text-right font-medium text-[var(--text-secondary)]">{t('admin.actions', '操作')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[var(--border-color)]">
            {loading ? (
              <tr>
                <td colSpan={7} className="px-4 py-8 text-center text-[var(--text-secondary)] animate-pulse">
                  {t('common.loading', '加载中...')}
                </td>
              </tr>
            ) : codes.length === 0 ? (
              <tr>
                <td colSpan={7} className="px-4 py-8 text-center text-[var(--text-secondary)]">
                  {t('inviteCodes.empty', '暂无邀请码')}
                </td>
              </tr>
            ) : codes.map((inv) => {
              const status = inviteStatus(inv);
              return (
                <tr key={inv.id} className="hover:bg-[var(--bg-secondary)]">
                  <td className="px-4 py-3 font-mono text-xs">…{inv.code_hint || inv.id.slice(0, 6)}</td>
                  <td className="px-4 py-3">{inv.role}</td>
                  <td className="px-4 py-3">{inv.use_count} / {inv.max_uses}</td>
                  <td className="px-4 py-3 text-[var(--text-secondary)]">{formatTime(inv.expires_at)}</td>
                  <td className="px-4 py-3">
                    <span className={`inline-flex px-2 py-0.5 rounded-full text-xs ${
                      status === 'active'
                        ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400'
                        : 'bg-[var(--bg-secondary)] text-[var(--text-secondary)]'
                    }`}>
                      {statusLabel(status)}
                    </span>
                  </td>
                  <td className="px-4 py-3 text-[var(--text-secondary)] max-w-[12rem] truncate" title={inv.note}>
                    {inv.note || '—'}
                  </td>
                  <td className="px-4 py-3 text-right">
                    <div className="flex items-center justify-end gap-2">
                      <button
                        type="button"
                        onClick={() => void handleToggleDisabled(inv)}
                        className="text-xs px-2 py-1 rounded border border-[var(--border-color)] text-[var(--text-secondary)] hover:bg-[var(--bg-secondary)]"
                        title={inv.disabled ? t('inviteCodes.enable', '启用') : t('inviteCodes.disable', '禁用')}
                      >
                        {inv.disabled ? <Check className="w-3.5 h-3.5" /> : <Ban className="w-3.5 h-3.5" />}
                      </button>
                      <button
                        type="button"
                        onClick={() => setDeleteConfirm(inv.id)}
                        className="text-xs px-2 py-1 rounded border border-red-200 text-red-600 hover:bg-red-50 dark:border-red-800"
                      >
                        <Trash2 className="w-3.5 h-3.5" />
                      </button>
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      {createdPlain && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 backdrop-blur-sm p-4">
          <div className="w-full max-w-md rounded-xl bg-[var(--bg-card)] border border-[var(--border-color)] shadow-2xl p-6">
            <h3 className="text-sm font-semibold text-[var(--text-primary)] mb-2">{t('inviteCodes.plainTitle', '邀请码已生成')}</h3>
            <p className="text-xs text-[var(--text-secondary)] mb-4">{t('inviteCodes.plainWarning', '明文仅显示一次，请立即复制并妥善分发')}</p>
            <div className="p-3 rounded-lg bg-[var(--bg-secondary)] font-mono text-sm break-all select-all">{createdPlain.code}</div>
            <div className="flex flex-wrap gap-2 mt-4">
              <button
                type="button"
                onClick={() => void navigator.clipboard.writeText(createdPlain.code).then(() => addToast('success', t('inviteCodes.codeCopied', '邀请码已复制')))}
                className="flex items-center gap-1 px-3 py-1.5 text-xs rounded-lg border border-[var(--border-color)]"
              >
                <Copy className="w-3.5 h-3.5" />
                {t('inviteCodes.copyCode', '复制码')}
              </button>
              <button
                type="button"
                onClick={() => void copyRegisterLink(createdPlain.code)}
                className="flex items-center gap-1 px-3 py-1.5 text-xs rounded-lg bg-[var(--accent-600)] text-white"
              >
                <Copy className="w-3.5 h-3.5" />
                {t('inviteCodes.copyLink', '复制注册链接')}
              </button>
            </div>
            <button
              type="button"
              onClick={() => setCreatedPlain(null)}
              className="mt-6 w-full px-3 py-2 text-sm rounded-lg border border-[var(--border-color)]"
            >
              {t('common.close', '关闭')}
            </button>
          </div>
        </div>
      )}

      {deleteConfirm && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 backdrop-blur-sm">
          <div className="w-80 rounded-xl bg-[var(--bg-card)] border border-[var(--border-color)] shadow-2xl p-6">
            <h3 className="text-sm font-semibold text-[var(--text-primary)] mb-2">{t('inviteCodes.deleteTitle', '删除邀请码')}</h3>
            <p className="text-sm text-[var(--text-secondary)] mb-5">{t('inviteCodes.deleteConfirm', '删除后无法恢复，已分发的码将失效。')}</p>
            <div className="flex justify-end gap-2">
              <button type="button" onClick={() => setDeleteConfirm(null)} className="px-3 py-1.5 text-xs rounded-lg border border-[var(--border-color)]">
                {t('common.cancel', '取消')}
              </button>
              <button type="button" onClick={() => void handleDeleteConfirmed()} className="px-3 py-1.5 text-xs rounded-lg bg-red-600 text-white">
                {t('common.delete', '删除')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

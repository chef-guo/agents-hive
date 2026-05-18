import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Search, Shield, Ban, ChevronLeft, ChevronRight, Trash2 } from 'lucide-react';
import { useNodeClient } from '../../hooks/useNodeClient';
import { useToastStore } from '../../store/toast';
import type { AdminUser } from '../../types/api';

export function UserList() {
  const { t } = useTranslation();
  const client = useNodeClient();
  const addToast = useToastStore((s) => s.addToast);

  const [users, setUsers] = useState<AdminUser[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [query, setQuery] = useState('');
  const [loading, setLoading] = useState(false);
  const [editingQuota, setEditingQuota] = useState<{ id: string; value: string } | null>(null);
  const [confirm, setConfirm] = useState<{ user: AdminUser; action: 'role' | 'status' | 'delete' } | null>(null);

  const size = 20;

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await client.adminListUsers(query, page, size);
      setUsers(res.users ?? []);
      setTotal(res.total ?? 0);
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : '加载用户列表失败');
    } finally {
      setLoading(false);
    }
  }, [client, query, page, addToast]);

  useEffect(() => { load(); }, [load]);

  const handleRoleToggle = async (user: AdminUser) => {
    setConfirm({ user, action: 'role' });
  };

  const handleStatusToggle = async (user: AdminUser) => {
    setConfirm({ user, action: 'status' });
  };

  const handleConfirm = async () => {
    if (!confirm) return;
    const { user, action } = confirm;
    setConfirm(null);
    if (action === 'role') {
      const newRole = user.role === 'admin' ? 'user' : 'admin';
      try {
        await client.adminUpdateUser(user.id, { role: newRole });
        addToast('success', `已将 ${user.display_name} 的角色改为 ${newRole}`);
        load();
      } catch (e: unknown) {
        addToast('error', e instanceof Error ? e.message : '更新角色失败');
      }
    } else if (action === 'status') {
      const newStatus = user.status === 'active' ? 'disabled' : 'active';
      try {
        await client.adminUpdateUser(user.id, { status: newStatus });
        addToast('success', `已${newStatus === 'active' ? '启用' : '禁用'} ${user.display_name}`);
        load();
      } catch (e: unknown) {
        addToast('error', e instanceof Error ? e.message : '更新状态失败');
      }
    } else {
      try {
        await client.adminDeleteUser(user.id);
        addToast('success', t('admin.userDeleted', '用户已删除'));
        load();
      } catch (e: unknown) {
        addToast('error', e instanceof Error ? e.message : t('admin.userDeleteFailed', '删除用户失败'));
      }
    }
  };

  const handleQuotaSave = async (userId: string) => {
    if (!editingQuota || editingQuota.id !== userId) return;
    const quota = parseInt(editingQuota.value, 10);
    if (isNaN(quota) || quota < 0) {
      addToast('error', '配额必须为非负整数');
      return;
    }
    try {
      await client.adminUpdateQuota(userId, quota);
      addToast('success', '配额已更新');
      setEditingQuota(null);
      load();
    } catch (e: unknown) {
      addToast('error', e instanceof Error ? e.message : '更新配额失败');
    }
  };

  const totalPages = Math.max(1, Math.ceil(total / size));

  return (
    <div className="p-6 max-w-6xl mx-auto">
      <div className="mb-6">
        <h1 className="text-xl font-semibold text-[var(--text-primary)]">{t('admin.users', '用户管理')}</h1>
        <p className="text-sm text-[var(--text-secondary)] mt-1">{t('admin.usersDesc', '管理系统用户的角色、状态和 Token 配额')}</p>
      </div>

      {/* 搜索栏 */}
      <div className="flex items-center gap-3 mb-4">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-[var(--text-secondary)]" />
          <input
            type="text"
            value={query}
            onChange={(e) => { setQuery(e.target.value); setPage(1); }}
            placeholder={t('admin.searchUsers', '搜索用户名或邮箱...')}
            className="w-full pl-9 pr-3 py-2 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] placeholder:text-[var(--text-secondary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)]"
          />
        </div>
        <span className="text-sm text-[var(--text-secondary)]">{t('admin.totalUsers', '共 {{count}} 位用户', { count: total })}</span>
      </div>

      {/* 表格 */}
      <div className="rounded-xl border border-[var(--border-color)] overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-[var(--bg-secondary)]">
            <tr>
              <th className="px-4 py-3 text-left font-medium text-[var(--text-secondary)]">{t('admin.user', '用户')}</th>
              <th className="px-4 py-3 text-left font-medium text-[var(--text-secondary)]">{t('admin.provider', '登录方式')}</th>
              <th className="px-4 py-3 text-left font-medium text-[var(--text-secondary)]">{t('admin.role', '角色')}</th>
              <th className="px-4 py-3 text-left font-medium text-[var(--text-secondary)]">{t('admin.status', '状态')}</th>
              <th className="px-4 py-3 text-left font-medium text-[var(--text-secondary)]">{t('admin.quota', 'Token 配额')}</th>
              <th className="px-4 py-3 text-left font-medium text-[var(--text-secondary)]">{t('admin.used', '已用')}</th>
              <th className="px-4 py-3 text-right font-medium text-[var(--text-secondary)]">{t('admin.actions', '操作')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[var(--border-color)]">
            {loading ? (
              <tr>
                <td colSpan={7} className="px-4 py-8 text-center text-[var(--text-secondary)] text-sm animate-pulse">
                  {t('common.loading', '加载中...')}
                </td>
              </tr>
            ) : users.length === 0 ? (
              <tr>
                <td colSpan={7} className="px-4 py-8 text-center text-[var(--text-secondary)] text-sm">
                  {t('admin.noUsers', '暂无用户')}
                </td>
              </tr>
            ) : users.map((user) => (
              <tr key={user.id} className="hover:bg-[var(--bg-secondary)] transition-colors">
                <td className="px-4 py-3">
                  <div className="font-medium text-[var(--text-primary)]">{user.display_name || user.id.slice(0, 8)}</div>
                  <div className="text-xs text-[var(--text-secondary)]">{user.email}</div>
                </td>
                <td className="px-4 py-3 text-[var(--text-secondary)]">{user.auth_provider || '-'}</td>
                <td className="px-4 py-3">
                  <span className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs font-medium ${
                    user.role === 'admin'
                      ? 'bg-[var(--accent-100)] text-[var(--accent-700)] dark:bg-[var(--accent-light)] dark:text-[var(--accent-300)]'
                      : 'bg-[var(--bg-secondary)] text-[var(--text-secondary)]'
                  }`}>
                    {user.role === 'admin' && <Shield className="w-3 h-3" />}
                    {user.role}
                  </span>
                </td>
                <td className="px-4 py-3">
                  <span className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs font-medium ${
                    user.status === 'active'
                      ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400'
                      : 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
                  }`}>
                    {user.status === 'disabled' && <Ban className="w-3 h-3" />}
                    {user.status}
                  </span>
                </td>
                <td className="px-4 py-3">
                  {editingQuota?.id === user.id ? (
                    <div className="flex items-center gap-1">
                      <input
                        type="number"
                        min="0"
                        value={editingQuota.value}
                        onChange={(e) => setEditingQuota({ id: user.id, value: e.target.value })}
                        onKeyDown={(e) => { if (e.key === 'Enter') handleQuotaSave(user.id); if (e.key === 'Escape') setEditingQuota(null); }}
                        className="w-24 px-2 py-1 text-xs rounded border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-1 focus:ring-[var(--accent)]"
                        autoFocus
                      />
                      <button onClick={() => handleQuotaSave(user.id)} className="text-xs text-green-600 hover:text-green-700 px-1">✓</button>
                      <button onClick={() => setEditingQuota(null)} className="text-xs text-[var(--text-secondary)] hover:text-[var(--text-primary)] px-1">✕</button>
                    </div>
                  ) : (
                    <button
                      onClick={() => setEditingQuota({ id: user.id, value: String(user.token_quota) })}
                      className="text-[var(--text-primary)] hover:text-[var(--accent)] transition-colors"
                      title={t('admin.editQuota', '点击编辑配额')}
                    >
                      {user.token_quota === 0 ? <span className="text-[var(--text-secondary)]">∞</span> : user.token_quota.toLocaleString()}
                    </button>
                  )}
                </td>
                <td className="px-4 py-3 text-[var(--text-secondary)]">
                  {user.token_used.toLocaleString()}
                </td>
                <td className="px-4 py-3 text-right">
                  <div className="flex items-center justify-end gap-2">
                    <button
                      onClick={() => handleRoleToggle(user)}
                      className="text-xs px-2 py-1 rounded border border-[var(--border-color)] text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-secondary)] transition-colors"
                    >
                      {user.role === 'admin' ? t('admin.demote', '降为用户') : t('admin.promote', '提升为管理员')}
                    </button>
                    <button
                      onClick={() => handleStatusToggle(user)}
                      className={`text-xs px-2 py-1 rounded border transition-colors ${
                        user.status === 'active'
                          ? 'border-red-200 text-red-600 hover:bg-red-50 dark:border-red-800 dark:hover:bg-red-900/20'
                          : 'border-green-200 text-green-600 hover:bg-green-50 dark:border-green-800 dark:hover:bg-green-900/20'
                      }`}
                    >
                      {user.status === 'active' ? t('admin.disable', '禁用') : t('admin.enable', '启用')}
                    </button>
                    <button
                      type="button"
                      onClick={() => setConfirm({ user, action: 'delete' })}
                      className="text-xs px-2 py-1 rounded border border-red-200 text-red-600 hover:bg-red-50 dark:border-red-800 dark:hover:bg-red-900/20"
                      title={t('admin.deleteUser', '删除用户')}
                    >
                      <Trash2 className="w-3.5 h-3.5" />
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {/* 分页 */}
      {totalPages > 1 && (
        <div className="flex items-center justify-between mt-4">
          <span className="text-sm text-[var(--text-secondary)]">
            {t('admin.page', '第 {{page}} / {{total}} 页', { page, total: totalPages })}
          </span>
          <div className="flex items-center gap-2">
            <button
              onClick={() => setPage((p) => Math.max(1, p - 1))}
              disabled={page === 1}
              className="p-1.5 rounded-lg border border-[var(--border-color)] text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-secondary)] disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
            >
              <ChevronLeft className="w-4 h-4" />
            </button>
            <button
              onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
              disabled={page === totalPages}
              className="p-1.5 rounded-lg border border-[var(--border-color)] text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-secondary)] disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
            >
              <ChevronRight className="w-4 h-4" />
            </button>
          </div>
        </div>
      )}

      {/* 确认对话框 */}
      {confirm && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 backdrop-blur-sm">
          <div className="w-80 rounded-xl bg-[var(--bg-card)] border border-[var(--border-color)] shadow-2xl p-6">
            <h3 className="text-sm font-semibold text-[var(--text-primary)] mb-2">
              {confirm.action === 'role'
                ? (confirm.user.role === 'admin' ? t('admin.demoteTitle', '降为普通用户') : t('admin.promoteTitle', '提升为管理员'))
                : confirm.action === 'status'
                  ? (confirm.user.status === 'active' ? t('admin.disableTitle', '禁用用户') : t('admin.enableTitle', '启用用户'))
                  : t('admin.deleteUserTitle', '删除用户')}
            </h3>
            <p className="text-sm text-[var(--text-secondary)] mb-5">
              {confirm.action === 'role'
                ? t('admin.roleConfirm', '确定将 {{name}} 的角色改为 {{role}}？', {
                    name: confirm.user.display_name,
                    role: confirm.user.role === 'admin' ? 'user' : 'admin',
                  })
                : confirm.action === 'status'
                  ? t('admin.statusConfirm', '确定{{action}} {{name}}？', {
                      name: confirm.user.display_name,
                      action: confirm.user.status === 'active' ? t('admin.disable', '禁用') : t('admin.enable', '启用'),
                    })
                  : t('admin.deleteUserConfirm', '确定永久删除 {{name}}？此操作不可恢复。', { name: confirm.user.display_name })}
            </p>
            <div className="flex justify-end gap-2">
              <button
                onClick={() => setConfirm(null)}
                className="px-3 py-1.5 text-xs rounded-lg border border-[var(--border-color)] text-[var(--text-secondary)] hover:bg-[var(--bg-secondary)] transition-colors"
              >
                取消
              </button>
              <button
                onClick={handleConfirm}
                className={`px-3 py-1.5 text-xs rounded-lg text-white transition-colors ${
                  (confirm.action === 'status' && confirm.user.status === 'active') || confirm.action === 'delete'
                    ? 'bg-red-600 hover:bg-red-700'
                    : 'bg-[var(--accent-600)] hover:bg-[var(--accent-700)]'
                }`}
              >
                确认
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

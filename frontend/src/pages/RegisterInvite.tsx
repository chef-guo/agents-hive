import { useEffect, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { useAuthStore } from '../store/auth';

import { mapRegisterError } from '../utils/registerErrors';

export function RegisterInvite() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const { authEnabled, hasLocalRegister, inviteErrorWeakDistinction, setAuth, checkAuthEnabled } = useAuthStore();

  const [inviteCode, setInviteCode] = useState(searchParams.get('code') ?? '');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (authEnabled === null) {
      void checkAuthEnabled();
    }
  }, [authEnabled, checkAuthEnabled]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    if (!inviteCode.trim() || !email.trim() || !password) {
      setError('请填写邀请码、邮箱和密码');
      return;
    }
    setSubmitting(true);
    try {
      const resp = await fetch('/api/v1/auth/register', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          invite_code: inviteCode.trim(),
          email: email.trim(),
          password,
          display_name: displayName.trim() || undefined,
        }),
      });
      const data = await resp.json().catch(() => ({}));
      if (!resp.ok) {
        setError(mapRegisterError(data.error_code ?? '', data.error ?? '', inviteErrorWeakDistinction));
        return;
      }
      if (data.token && data.user) {
        setAuth(data.token, data.user);
        navigate('/', { replace: true });
      } else {
        navigate('/login', { replace: true });
      }
    } catch {
      setError('网络错误，请重试');
    } finally {
      setSubmitting(false);
    }
  };

  if (authEnabled === null) {
    return (
      <div className="flex items-center justify-center min-h-screen bg-[var(--bg-primary)]">
        <div className="text-[var(--text-secondary)] animate-pulse">加载中...</div>
      </div>
    );
  }

  if (authEnabled === false || !hasLocalRegister) {
    return (
      <div className="flex items-center justify-center min-h-screen bg-[var(--bg-primary)] px-4">
        <div className="w-full max-w-md p-8 bg-[var(--bg-card)] rounded-[16px] border border-[var(--border-color)] text-center">
          <p className="text-sm text-[var(--text-secondary)]">认证服务未就绪，暂无法注册</p>
          <Link to="/login" className="mt-4 inline-block text-sm text-[var(--accent-600)]">返回登录</Link>
        </div>
      </div>
    );
  }

  return (
    <div className="flex items-center justify-center min-h-screen bg-[var(--bg-primary)] px-4">
      <div className="w-full max-w-sm p-8 bg-[var(--bg-card)] rounded-[16px] shadow-sm border border-[var(--border-color)]">
        <h1 className="text-xl font-semibold text-center text-[var(--text-primary)] mb-6">持邀请码注册</h1>
        <form onSubmit={handleSubmit} className="space-y-3">
          <input
            type="text"
            placeholder="邀请码"
            value={inviteCode}
            onChange={(e) => setInviteCode(e.target.value)}
            className="w-full px-4 py-3 rounded-[10px] border border-[var(--border-color)] bg-[var(--bg-primary)] text-sm"
          />
          <input
            type="email"
            placeholder="邮箱"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            className="w-full px-4 py-3 rounded-[10px] border border-[var(--border-color)] bg-[var(--bg-primary)] text-sm"
            autoComplete="email"
          />
          <input
            type="text"
            placeholder="显示名（可选）"
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
            className="w-full px-4 py-3 rounded-[10px] border border-[var(--border-color)] bg-[var(--bg-primary)] text-sm"
          />
          <input
            type="password"
            placeholder="密码（至少 8 位）"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="w-full px-4 py-3 rounded-[10px] border border-[var(--border-color)] bg-[var(--bg-primary)] text-sm"
            autoComplete="new-password"
          />
          {error && <p className="text-xs text-red-500">{error}</p>}
          <button
            type="submit"
            disabled={submitting}
            className="w-full px-4 py-3 rounded-[10px] bg-[var(--accent-600)] text-white text-sm font-medium disabled:opacity-50"
          >
            {submitting ? '提交中...' : '注册'}
          </button>
        </form>
        <p className="mt-6 text-center text-xs text-[var(--text-secondary)]">
          已有账号？{' '}
          <Link to="/login" className="text-[var(--accent-600)]">登录</Link>
        </p>
      </div>
    </div>
  );
}

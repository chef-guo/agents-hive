import { useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuthStore } from '../store/auth';
import { mapRegisterError } from '../utils/registerErrors';

export function Register() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const {
    authEnabled,
    allowPublicRegistration,
    hasLocalRegister,
    inviteErrorWeakDistinction,
    setAuth,
    checkAuthEnabled,
  } = useAuthStore();

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
    if (!email.trim() || !password) {
      setError(t('register.fillRequired', '请填写邮箱和密码'));
      return;
    }
    setSubmitting(true);
    try {
      const resp = await fetch('/api/v1/auth/register', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
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
      setError(t('register.networkError', '网络错误，请重试'));
    } finally {
      setSubmitting(false);
    }
  };

  if (authEnabled === null) {
    return (
      <div className="flex items-center justify-center min-h-screen bg-[var(--bg-primary)]">
        <div className="text-[var(--text-secondary)] animate-pulse">{t('common.loading', '加载中...')}</div>
      </div>
    );
  }

  if (authEnabled === false || !hasLocalRegister || !allowPublicRegistration) {
    return (
      <div className="flex items-center justify-center min-h-screen bg-[var(--bg-primary)] px-4">
        <div className="w-full max-w-md p-8 bg-[var(--bg-card)] rounded-[16px] border border-[var(--border-color)] text-center">
          <p className="text-sm text-[var(--text-secondary)]">{t('register.closed', '当前未开放公开注册')}</p>
          <Link to="/register/invite" className="mt-3 inline-block text-sm text-[var(--accent-600)]">
            {t('register.inviteLink', '持邀请码注册')}
          </Link>
          <Link to="/login" className="mt-4 block text-sm text-[var(--accent-600)]">
            {t('register.backLogin', '返回登录')}
          </Link>
        </div>
      </div>
    );
  }

  return (
    <div className="flex items-center justify-center min-h-screen bg-[var(--bg-primary)] px-4">
      <div className="w-full max-w-sm p-8 bg-[var(--bg-card)] rounded-[16px] shadow-sm border border-[var(--border-color)]">
        <h1 className="text-xl font-semibold text-center text-[var(--text-primary)] mb-6">{t('register.title', '注册账号')}</h1>
        <form onSubmit={handleSubmit} className="space-y-3">
          <input
            type="email"
            placeholder={t('register.email', '邮箱')}
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            className="w-full px-4 py-3 rounded-[10px] border border-[var(--border-color)] bg-[var(--bg-primary)] text-sm"
            autoComplete="email"
          />
          <input
            type="text"
            placeholder={t('register.displayName', '显示名（可选）')}
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
            className="w-full px-4 py-3 rounded-[10px] border border-[var(--border-color)] bg-[var(--bg-primary)] text-sm"
          />
          <input
            type="password"
            placeholder={t('register.password', '密码（至少 8 位）')}
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
            {submitting ? t('register.submitting', '提交中...') : t('register.submit', '注册')}
          </button>
        </form>
        <p className="mt-6 text-center text-xs text-[var(--text-secondary)]">
          {t('register.hasAccount', '已有账号？')}{' '}
          <Link to="/login" className="text-[var(--accent-600)]">{t('nav.login', '登录')}</Link>
        </p>
        <p className="mt-2 text-center text-xs text-[var(--text-secondary)]">
          <Link to="/register/invite" className="text-[var(--accent-600)]">{t('register.inviteLink', '持邀请码注册')}</Link>
        </p>
      </div>
    </div>
  );
}

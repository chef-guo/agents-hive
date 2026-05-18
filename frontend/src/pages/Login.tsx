import { useState, useEffect } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuthStore } from '../store/auth';
import type { AuthProvider } from '../store/auth';

export function Login() {
  const { t } = useTranslation();
  const { authEnabled } = useAuthStore();
  const checkAuthEnabled = useAuthStore((s) => s.checkAuthEnabled);
  const fetchProviders = useAuthStore((s) => s.fetchProviders);
  const setAuth = useAuthStore((s) => s.setAuth);
  const navigate = useNavigate();

  const [providers, setProviders] = useState<AuthProvider[]>([]);
  const [loadingProviders, setLoadingProviders] = useState(true);

  // LDAP 表单状态
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [ldapError, setLdapError] = useState('');
  const [ldapSubmitting, setLdapSubmitting] = useState(false);

  // 进入登录页时若尚未拉取 auth 开关，补一次检测
  useEffect(() => {
    if (authEnabled === null) {
      void checkAuthEnabled();
    }
  }, [authEnabled, checkAuthEnabled]);

  // 加载 provider 列表
  useEffect(() => {
    if (authEnabled !== true) return;
    fetchProviders().then((p) => {
      setProviders(p);
      setLoadingProviders(false);
    });
  }, [authEnabled, fetchProviders]);

  // OAuth 按钮点击 → 跳转后端
  const handleOAuth = (providerName: string) => {
    window.location.href = `/api/v1/auth/login?provider=${encodeURIComponent(providerName)}`;
  };

  // LDAP 表单提交
  const handleLDAP = async (e: React.FormEvent) => {
    e.preventDefault();
    setLdapError('');

    if (!username.trim() || !password) {
      setLdapError('请输入用户名和密码');
      return;
    }

    if (!/^[a-zA-Z0-9._@-]+$/.test(username)) {
      setLdapError('用户名包含非法字符');
      return;
    }

    if (ldapSubmitting) return;
    setLdapSubmitting(true);

    try {
      const providerName = ldapProvider?.name || (showLocalForm ? 'local' : 'ldap');
      const resp = await fetch('/api/v1/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ provider: providerName, username: username.trim(), password }),
      });

      if (!resp.ok) {
        if (resp.status === 401) {
          setLdapError('用户名或密码错误');
        } else if (resp.status === 502 || resp.status === 503) {
          setLdapError('LDAP 服务不可用，请联系管理员');
        } else {
          setLdapError('登录失败，请重试');
        }
        return;
      }

      const data = await resp.json();
      setAuth(data.token, data.user);
      navigate('/', { replace: true });
    } catch {
      setLdapError('网络超时，请重试');
    } finally {
      setLdapSubmitting(false);
    }
  };

  const { hasLocalRegister, allowPublicRegistration } = useAuthStore();
  const oauthProviders = providers.filter((p) => p.provider_type !== 'ldap' && p.provider_type !== 'local');
  const ldapProvider = providers.find((p) => p.provider_type === 'ldap');
  const showLocalForm = hasLocalRegister || providers.some((p) => p.provider_type === 'local');

  if (authEnabled === null) {
    return (
      <div className="flex items-center justify-center min-h-screen bg-[var(--bg-primary)]">
        <div className="text-[var(--text-secondary)] animate-pulse">加载中...</div>
      </div>
    );
  }

  if (authEnabled === false) {
    return (
      <div className="flex items-center justify-center min-h-screen bg-[var(--bg-primary)] px-4">
        <div className="w-full max-w-md p-8 bg-[var(--bg-card)] rounded-[16px] shadow-sm border border-[var(--border-color)] text-center">
          <h1 className="text-xl font-semibold text-[var(--text-primary)] font-[Geist]">
            {t('loginPage.authDisabledTitle')}
          </h1>
          <p className="mt-3 text-sm text-[var(--text-secondary)] leading-relaxed">
            {t('loginPage.authDisabledDesc')}
          </p>
          <Link
            to="/"
            className="mt-6 inline-flex items-center justify-center rounded-[10px] bg-[var(--accent-600)] px-5 py-2.5 text-sm font-medium text-white hover:bg-[var(--accent-700)] transition-colors"
          >
            {t('loginPage.backHome')}
          </Link>
        </div>
      </div>
    );
  }

  return (
    <div className="flex items-center justify-center min-h-screen bg-[var(--bg-primary)]">
      <div className="w-full max-w-sm p-8 bg-[var(--bg-card)] rounded-[16px] shadow-sm border border-[var(--border-color)]">
        {/* Logo */}
        <div className="text-center mb-8">
          <h1 className="text-2xl font-semibold text-[var(--text-primary)] font-[Geist]">Hive</h1>
          <p className="text-sm text-[var(--text-secondary)] mt-1">企业 Agent 控制中心</p>
        </div>

        {loadingProviders ? (
          <div className="text-center text-[var(--text-secondary)] animate-pulse">加载中...</div>
        ) : providers.length === 0 && !showLocalForm ? (
          <p className="text-center text-[var(--text-secondary)]">请联系管理员配置登录方式</p>
        ) : (
          <>
            {/* OAuth 按钮 */}
            {oauthProviders.map((p) => (
              <button
                key={p.name}
                onClick={() => handleOAuth(p.name)}
                className="w-full mb-3 px-4 py-3 rounded-[10px] border border-[var(--border-color)] text-[var(--text-primary)] hover:bg-[var(--bg-hover)] transition-colors text-sm"
              >
                {p.provider_type === 'feishu' ? '飞书登录' : p.provider_type === 'dingtalk' ? '钉钉登录' : p.name}
              </button>
            ))}

            {/* 分隔线 */}
            {oauthProviders.length > 0 && (ldapProvider || showLocalForm) && (
              <div className="flex items-center my-4">
                <div className="flex-1 border-t border-[var(--border-color)]" />
                <span className="px-3 text-xs text-[var(--text-secondary)]">或</span>
                <div className="flex-1 border-t border-[var(--border-color)]" />
              </div>
            )}

            {/* LDAP / 本地账密 */}
            {(ldapProvider || showLocalForm) && (
              <form onSubmit={handleLDAP}>
                <input
                  type="text"
                  placeholder={showLocalForm && !ldapProvider ? '邮箱或用户名' : '用户名'}
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  className="w-full mb-3 px-4 py-3 rounded-[10px] border border-[var(--border-color)] bg-[var(--bg-primary)] text-sm text-[var(--text-primary)] placeholder:text-[var(--text-secondary)]"
                  autoComplete="username"
                />
                <input
                  type="password"
                  placeholder="密码"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="w-full mb-3 px-4 py-3 rounded-[10px] border border-[var(--border-color)] bg-[var(--bg-primary)] text-sm text-[var(--text-primary)] placeholder:text-[var(--text-secondary)]"
                  autoComplete="current-password"
                />
                {ldapError && (
                  <p className="text-xs text-red-500 mb-3">{ldapError}</p>
                )}
                <button
                  type="submit"
                  disabled={ldapSubmitting}
                  className="w-full px-4 py-3 rounded-[10px] bg-[var(--accent-600)] text-white text-sm font-medium hover:bg-[var(--accent-700)] transition-colors disabled:opacity-50"
                >
                  {ldapSubmitting ? '登录中...' : '登录'}
                </button>
              </form>
            )}

            {showLocalForm && (
              <p className="mt-6 text-center text-xs text-[var(--text-secondary)] space-x-3">
                {allowPublicRegistration && (
                  <Link to="/register" className="text-[var(--accent-600)] hover:underline">
                    {t('register.publicLink', '注册账号')}
                  </Link>
                )}
                <Link to="/register/invite" className="text-[var(--accent-600)] hover:underline">
                  {t('register.inviteLink', '持邀请码注册')}
                </Link>
              </p>
            )}
          </>
        )}
      </div>
    </div>
  );
}

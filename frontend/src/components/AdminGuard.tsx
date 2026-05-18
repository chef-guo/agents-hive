import { useEffect, useRef } from 'react';
import { Navigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuthStore } from '../store/auth';
import { useToastStore } from '../store/toast';

export function AdminGuard({ children }: { children: React.ReactNode }) {
  const { t } = useTranslation();
  const { user, loading, authEnabled } = useAuthStore();
  const addToast = useToastStore((s) => s.addToast);
  const warnedRef = useRef(false);

  useEffect(() => {
    if (loading || authEnabled === null) return;
    if (authEnabled === false) return;
    if (!user) {
      if (warnedRef.current) return;
      warnedRef.current = true;
      addToast('warning', t('nav.loginRequiresAuthHint', '管理后台需启用登录认证'));
      return;
    }
    if (user.role !== 'admin') {
      if (warnedRef.current) return;
      warnedRef.current = true;
      addToast('warning', '需要管理员权限才能访问该页面');
    }
  }, [loading, authEnabled, user, addToast, t]);

  if (loading || authEnabled === null) {
    return (
      <div className="flex items-center justify-center h-screen">
        <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-gray-900" />
      </div>
    );
  }

  if (authEnabled === false) {
    return (
      <div className="flex items-center justify-center min-h-screen bg-[var(--bg-primary)] px-4">
        <div className="w-full max-w-md p-8 bg-[var(--bg-card)] rounded-[16px] border border-[var(--border-color)] text-center">
          <h1 className="text-lg font-semibold text-[var(--text-primary)]">{t('loginPage.authDisabledTitle')}</h1>
          <p className="mt-3 text-sm text-[var(--text-secondary)]">{t('loginPage.authDisabledDesc')}</p>
        </div>
      </div>
    );
  }

  if (!user) {
    return <Navigate to="/login" replace />;
  }
  if (user.role !== 'admin') {
    return <Navigate to="/" replace />;
  }

  return <>{children}</>;
}

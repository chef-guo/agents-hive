import { useEffect, type ReactNode } from 'react';
import { Navigate, useNavigate } from 'react-router-dom';
import { useAuthStore } from '../store/auth';

export function AuthGuard({ children }: { children: ReactNode }) {
  const { loading, authEnabled, authError, user, token } = useAuthStore();
  const checkAuthEnabled = useAuthStore((s) => s.checkAuthEnabled);
  const checkAuth = useAuthStore((s) => s.checkAuth);
  const navigate = useNavigate();

  useEffect(() => {
    const init = async () => {
      if (authEnabled === null) {
        const enabled = await checkAuthEnabled();
        if (!enabled) return;
        if (enabled && !user) {
          const valid = await checkAuth();
          if (!valid) navigate('/login', { replace: true });
        }
        return;
      }
      if (authEnabled && !user) {
        const valid = await checkAuth();
        if (!valid) {
          navigate('/login', { replace: true });
        }
      }
    };
    void init();
  }, [authEnabled, user, checkAuthEnabled, checkAuth, navigate]);

  if (authError) {
    return (
      <div className="flex items-center justify-center h-screen">
        <div className="text-center">
          <p className="text-[var(--text-secondary)] mb-4">{authError}</p>
          <button
            type="button"
            onClick={() => window.location.reload()}
            className="px-4 py-2 rounded-[10px] bg-[var(--accent-600)] text-white"
          >
            重试
          </button>
        </div>
      </div>
    );
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center h-screen">
        <div className="animate-pulse text-[var(--text-secondary)]">加载中...</div>
      </div>
    );
  }

  if (authEnabled === false) {
    return (
      <div className="flex items-center justify-center min-h-screen bg-[var(--bg-primary)] px-4">
        <div className="w-full max-w-md p-8 bg-[var(--bg-card)] rounded-[16px] border border-[var(--border-color)] text-center">
          <h1 className="text-lg font-semibold text-[var(--text-primary)]">服务暂不可用</h1>
          <p className="mt-3 text-sm text-[var(--text-secondary)]">
            认证引擎未就绪，请检查 PostgreSQL 与服务器配置后重试。
          </p>
          <button
            type="button"
            onClick={() => window.location.reload()}
            className="mt-6 px-4 py-2 rounded-[10px] bg-[var(--accent-600)] text-white text-sm"
          >
            重试
          </button>
        </div>
      </div>
    );
  }

  if (authEnabled === true && !user && !token) {
    return <Navigate to="/login" replace />;
  }

  if (authEnabled === null) {
    return (
      <div className="flex items-center justify-center h-screen">
        <div className="animate-pulse text-[var(--text-secondary)]">加载中...</div>
      </div>
    );
  }

  return <>{children}</>;
}

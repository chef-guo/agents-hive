import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { AuthGuard } from '../AuthGuard';
import { useAuthStore } from '../../store/auth';

// mock useNavigate
const mockNavigate = vi.fn();
vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>();
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

// mock auth store
vi.mock('../../store/auth', () => ({
  useAuthStore: vi.fn(),
}));

const mockUseAuthStore = vi.mocked(useAuthStore);

function buildStore(overrides: Partial<ReturnType<typeof useAuthStore>>) {
  const defaults = {
    user: null,
    token: null,
    loading: false,
    authEnabled: null,
    allowPublicRegistration: false,
    inviteErrorWeakDistinction: false,
    hasLocalRegister: false,
    authError: null,
    checkAuthEnabled: vi.fn().mockResolvedValue(false),
    checkAuth: vi.fn().mockResolvedValue(false),
    setAuth: vi.fn(),
    clearAuth: vi.fn(),
    logout: vi.fn(),
    fetchProviders: vi.fn().mockResolvedValue([]),
  };
  return { ...defaults, ...overrides };
}

beforeEach(() => {
  vi.clearAllMocks();
  mockNavigate.mockReset();
});

function renderGuard(storeState: ReturnType<typeof buildStore>) {
  // useAuthStore 既作为 hook 调用，也作为选择器调用
  mockUseAuthStore.mockImplementation((selector?: (s: ReturnType<typeof buildStore>) => unknown) => {
    if (typeof selector === 'function') return selector(storeState);
    return storeState;
  });

  return render(
    <MemoryRouter>
      <AuthGuard>
        <div>protected content</div>
      </AuthGuard>
    </MemoryRouter>
  );
}

describe('AuthGuard', () => {
  it('authEnabled=false → 显示认证未就绪（不绕过鉴权）', async () => {
    const store = buildStore({ authEnabled: false, loading: false });
    renderGuard(store);

    await waitFor(() => {
      expect(screen.getByText('服务暂不可用')).toBeInTheDocument();
      expect(screen.getByText(/认证引擎未就绪/)).toBeInTheDocument();
    });
    expect(screen.queryByText('protected content')).not.toBeInTheDocument();
  });

  it('authEnabled=true + user 有效 → 渲染 children', async () => {
    const user = { id: '1', display_name: 'Test', email: '', avatar_url: '', department: '', role: 'user' as const };
    const store = buildStore({ authEnabled: true, loading: false, user, token: 'tok' });
    renderGuard(store);

    await waitFor(() => {
      expect(screen.getByText('protected content')).toBeInTheDocument();
    });
  });

  it('authEnabled=true + user 无效 → redirect /login', async () => {
    const store = buildStore({
      authEnabled: true,
      loading: false,
      user: null,
      token: null,
      checkAuth: vi.fn().mockResolvedValue(false),
    });
    renderGuard(store);

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith('/login', { replace: true });
    });
  });

  it('authError → 显示错误页', async () => {
    const store = buildStore({ authError: '服务不可用，请稍后重试', loading: false });
    renderGuard(store);

    expect(screen.getByText('服务不可用，请稍后重试')).toBeInTheDocument();
    expect(screen.getByText('重试')).toBeInTheDocument();
  });

  it('loading=true → 显示加载中', () => {
    const store = buildStore({ loading: true });
    renderGuard(store);

    expect(screen.getByText('加载中...')).toBeInTheDocument();
  });

  it('authEnabled=null → checkAuthEnabled 被调用', async () => {
    const checkAuthEnabled = vi.fn().mockResolvedValue(false);
    const store = buildStore({ authEnabled: null, loading: true, checkAuthEnabled });
    renderGuard(store);

    await waitFor(() => {
      expect(checkAuthEnabled).toHaveBeenCalledOnce();
    });
  });

  it('authEnabled=null + enabled=true + 无 user → checkAuth 被调用（修复后的新路径）', async () => {
    const checkAuth = vi.fn().mockResolvedValue(false);
    const checkAuthEnabled = vi.fn().mockResolvedValue(true);
    const store = buildStore({
      authEnabled: null,
      loading: true,
      user: null,
      token: null,
      checkAuthEnabled,
      checkAuth,
    });
    renderGuard(store);

    await waitFor(() => {
      expect(checkAuth).toHaveBeenCalledOnce();
    });
  });
});

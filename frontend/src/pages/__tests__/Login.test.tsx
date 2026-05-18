import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import i18n from '../../i18n';
import { Login } from '../Login';
import { useAuthStore } from '../../store/auth';

const mockNavigate = vi.fn();
vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>();
  return { ...actual, useNavigate: () => mockNavigate };
});

vi.mock('../../store/auth', () => ({
  useAuthStore: vi.fn(),
}));

const mockUseAuthStore = vi.mocked(useAuthStore);

function buildStore(overrides = {}) {
  const defaults = {
    authEnabled: true,
    checkAuthEnabled: vi.fn().mockResolvedValue(true),
    fetchProviders: vi.fn().mockResolvedValue([]),
    setAuth: vi.fn(),
  };
  return { ...defaults, ...overrides };
}

function renderLogin(storeState: ReturnType<typeof buildStore>) {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  mockUseAuthStore.mockImplementation((selector?: (s: any) => unknown) => {
    if (typeof selector === 'function') return selector(storeState);
    return storeState;
  });
  return render(<MemoryRouter><Login /></MemoryRouter>);
}

beforeEach(() => {
  vi.clearAllMocks();
  mockNavigate.mockReset();
});

describe('Login', () => {
  it('authEnabled=false → 显示未启用说明，不自动跳首页', () => {
    const store = buildStore({ authEnabled: false });
    renderLogin(store);
    expect(screen.getByText(i18n.t('loginPage.authDisabledTitle'))).toBeInTheDocument();
    expect(mockNavigate).not.toHaveBeenCalledWith('/', { replace: true });
  });

  it('authEnabled=null → 请求 checkAuthEnabled', async () => {
    const checkAuthEnabled = vi.fn().mockResolvedValue(true);
    const store = buildStore({ authEnabled: null, checkAuthEnabled });
    renderLogin(store);
    await waitFor(() => expect(checkAuthEnabled).toHaveBeenCalled());
  });

  it('authEnabled=true → 加载 providers', async () => {
    const providers = [{ name: 'feishu', provider_type: 'feishu', enabled: true }];
    const store = buildStore({
      authEnabled: true,
      fetchProviders: vi.fn().mockResolvedValue(providers),
    });
    renderLogin(store);
    await waitFor(() => {
      expect(screen.getByText('飞书登录')).toBeInTheDocument();
    });
  });

  it('providers 为空 → 显示"请联系管理员"', async () => {
    const store = buildStore({
      authEnabled: true,
      fetchProviders: vi.fn().mockResolvedValue([]),
    });
    renderLogin(store);
    await waitFor(() => {
      expect(screen.getByText('请联系管理员配置登录方式')).toBeInTheDocument();
    });
  });

  it('LDAP 表单 — 空用户名 → 错误提示', async () => {
    const store = buildStore({
      authEnabled: true,
      fetchProviders: vi.fn().mockResolvedValue([{ name: 'ldap', provider_type: 'ldap', enabled: true }]),
    });
    renderLogin(store);
    await waitFor(() => screen.getByPlaceholderText('用户名'));

    fireEvent.click(screen.getByRole('button', { name: '登录' }));
    await waitFor(() => {
      expect(screen.getByText('请输入用户名和密码')).toBeInTheDocument();
    });
  });

  it('LDAP 表单 — 非法字符 → 错误提示', async () => {
    const store = buildStore({
      authEnabled: true,
      fetchProviders: vi.fn().mockResolvedValue([{ name: 'ldap', provider_type: 'ldap', enabled: true }]),
    });
    renderLogin(store);
    await waitFor(() => screen.getByPlaceholderText('用户名'));

    fireEvent.change(screen.getByPlaceholderText('用户名'), { target: { value: 'user<script>' } });
    fireEvent.change(screen.getByPlaceholderText('密码'), { target: { value: 'pass' } });
    fireEvent.click(screen.getByRole('button', { name: '登录' }));
    await waitFor(() => {
      expect(screen.getByText('用户名包含非法字符')).toBeInTheDocument();
    });
  });

  it('LDAP 表单 — 401 → "用户名或密码错误"', async () => {
    const store = buildStore({
      authEnabled: true,
      fetchProviders: vi.fn().mockResolvedValue([{ name: 'ldap', provider_type: 'ldap', enabled: true }]),
    });
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 401 }));
    renderLogin(store);
    await waitFor(() => screen.getByPlaceholderText('用户名'));

    fireEvent.change(screen.getByPlaceholderText('用户名'), { target: { value: 'testuser' } });
    fireEvent.change(screen.getByPlaceholderText('密码'), { target: { value: 'wrongpass' } });
    fireEvent.click(screen.getByRole('button', { name: '登录' }));
    await waitFor(() => {
      expect(screen.getByText('用户名或密码错误')).toBeInTheDocument();
    });
    vi.unstubAllGlobals();
  });

  it('LDAP 表单 — 502 → "LDAP 服务不可用"', async () => {
    const store = buildStore({
      authEnabled: true,
      fetchProviders: vi.fn().mockResolvedValue([{ name: 'ldap', provider_type: 'ldap', enabled: true }]),
    });
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 502 }));
    renderLogin(store);
    await waitFor(() => screen.getByPlaceholderText('用户名'));

    fireEvent.change(screen.getByPlaceholderText('用户名'), { target: { value: 'testuser' } });
    fireEvent.change(screen.getByPlaceholderText('密码'), { target: { value: 'pass' } });
    fireEvent.click(screen.getByRole('button', { name: '登录' }));
    await waitFor(() => {
      expect(screen.getByText('LDAP 服务不可用，请联系管理员')).toBeInTheDocument();
    });
    vi.unstubAllGlobals();
  });

  it('LDAP 表单 — 成功 → setAuth + navigate("/")', async () => {
    const setAuth = vi.fn();
    const store = buildStore({
      authEnabled: true,
      fetchProviders: vi.fn().mockResolvedValue([{ name: 'ldap', provider_type: 'ldap', enabled: true }]),
      setAuth,
    });
    const user = { id: '1', display_name: 'Test', email: '', avatar_url: '', department: '', role: 'user' };
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: vi.fn().mockResolvedValue({ token: 'jwt-token', user }),
    }));
    renderLogin(store);
    await waitFor(() => screen.getByPlaceholderText('用户名'));

    fireEvent.change(screen.getByPlaceholderText('用户名'), { target: { value: 'testuser' } });
    fireEvent.change(screen.getByPlaceholderText('密码'), { target: { value: 'pass' } });
    fireEvent.click(screen.getByRole('button', { name: '登录' }));
    await waitFor(() => {
      expect(setAuth).toHaveBeenCalledWith('jwt-token', user);
      expect(mockNavigate).toHaveBeenCalledWith('/', { replace: true });
    });
    vi.unstubAllGlobals();
  });

  it('LDAP 表单 — 网络错误 → "网络超时，请重试"', async () => {
    const store = buildStore({
      authEnabled: true,
      fetchProviders: vi.fn().mockResolvedValue([{ name: 'ldap', provider_type: 'ldap', enabled: true }]),
    });
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('Network Error')));
    renderLogin(store);
    await waitFor(() => screen.getByPlaceholderText('用户名'));

    fireEvent.change(screen.getByPlaceholderText('用户名'), { target: { value: 'testuser' } });
    fireEvent.change(screen.getByPlaceholderText('密码'), { target: { value: 'pass' } });
    fireEvent.click(screen.getByRole('button', { name: '登录' }));
    await waitFor(() => {
      expect(screen.getByText('网络超时，请重试')).toBeInTheDocument();
    });
    vi.unstubAllGlobals();
  });

  it('钉钉 provider → 显示"钉钉登录"', async () => {
    const store = buildStore({
      authEnabled: true,
      fetchProviders: vi.fn().mockResolvedValue([{ name: 'dingtalk', provider_type: 'dingtalk', enabled: true }]),
    });
    renderLogin(store);
    await waitFor(() => {
      expect(screen.getByText('钉钉登录')).toBeInTheDocument();
    });
  });
});

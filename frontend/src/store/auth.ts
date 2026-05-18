import { create } from 'zustand';
import { apiClient } from '../api/client';

interface User {
  id: string;
  display_name: string;
  email: string;
  avatar_url: string;
  department: string;
  role: 'user' | 'admin';
}

export interface AuthProvider {
  name: string;
  provider_type: 'feishu' | 'dingtalk' | 'ldap' | 'local';
  enabled: boolean;
}

interface AuthStatusResponse {
  enabled: boolean;
  allow_public_registration?: boolean;
  invite_error_weak_distinction?: boolean;
  has_local_register?: boolean;
}

interface AuthState {
  user: User | null;
  token: string | null;
  loading: boolean;
  authEnabled: boolean | null;
  allowPublicRegistration: boolean;
  inviteErrorWeakDistinction: boolean;
  hasLocalRegister: boolean;
  authError: string | null;

  setAuth: (token: string, user: User) => void;
  clearAuth: () => void;
  logout: () => void;
  checkAuth: () => Promise<boolean>;
  checkAuthEnabled: () => Promise<boolean>;
  fetchProviders: () => Promise<AuthProvider[]>;
}

let refreshPromise: Promise<string | null> | null = null;
let refreshGeneration = 0;
const DEFAULT_REFRESH_SKEW_MS = 60_000;

/** 取消进行中的 token 刷新（退出登录时调用，避免 refresh 完成后把 token 写回）。 */
export function cancelAuthRefresh() {
  refreshGeneration++;
  refreshPromise = null;
}

function decodeJWTPayload(token: string): { exp?: number } | null {
  const parts = token.split('.');
  if (parts.length < 2) return null;
  try {
    const normalized = parts[1].replace(/-/g, '+').replace(/_/g, '/');
    const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, '=');
    return JSON.parse(atob(padded)) as { exp?: number };
  } catch {
    return null;
  }
}

export function shouldRefreshToken(token: string | null, skewMs = DEFAULT_REFRESH_SKEW_MS): boolean {
  if (!token) return false;
  const payload = decodeJWTPayload(token);
  if (!payload?.exp) return true;
  return payload.exp * 1000 <= Date.now() + skewMs;
}

export const useAuthStore = create<AuthState>((set, get) => ({
  user: null,
  token: localStorage.getItem('auth_token'),
  loading: true,
  authEnabled: null,
  allowPublicRegistration: false,
  inviteErrorWeakDistinction: false,
  hasLocalRegister: false,
  authError: null,

  clearAuth: () => {
    localStorage.removeItem('auth_token');
    set({
      token: null,
      user: null,
      loading: false,
      authError: null,
    });
  },

  setAuth: (token, user) => {
    localStorage.setItem('auth_token', token);
    set({ token, user, loading: false, authEnabled: true, authError: null });
  },

  logout: () => {
    cancelAuthRefresh();
    get().clearAuth();
    // 硬跳转，断开 WS 并清空各页面内存态
    window.location.replace('/login');
  },

  checkAuthEnabled: async () => {
    try {
      const data = await apiClient.get<AuthStatusResponse>('/api/v1/auth/status');
      set({
        authEnabled: data.enabled,
        allowPublicRegistration: !!data.allow_public_registration,
        inviteErrorWeakDistinction: !!data.invite_error_weak_distinction,
        hasLocalRegister: data.has_local_register !== false && data.enabled,
        loading: data.enabled ? get().loading : false,
      });
      if (!data.enabled) {
        set({ loading: false });
      }
      return data.enabled;
    } catch (err) {
      if (err instanceof Error && 'code' in err && (err as { code: number }).code === 404) {
        set({ authEnabled: false, loading: false });
        return false;
      }
      try {
        const data = await apiClient.get<AuthStatusResponse>('/api/v1/auth/status');
        set({
          authEnabled: data.enabled,
          allowPublicRegistration: !!data.allow_public_registration,
          inviteErrorWeakDistinction: !!data.invite_error_weak_distinction,
          hasLocalRegister: data.has_local_register !== false && data.enabled,
        });
        if (!data.enabled) set({ loading: false });
        return data.enabled;
      } catch {
        set({ authError: '服务不可用，请稍后重试', loading: false });
        return false;
      }
    }
  },

  checkAuth: async () => {
    const token = get().token;
    if (!token) {
      set({ loading: false });
      return false;
    }
    try {
      const user = await apiClient.get<User>('/api/v1/auth/me');
      set({ user, loading: false });
      return true;
    } catch {
      get().clearAuth();
      return false;
    }
  },

  fetchProviders: async () => {
    try {
      const data = await apiClient.get<{ providers: AuthProvider[] }>('/api/v1/auth/providers');
      return data.providers ?? [];
    } catch {
      return [];
    }
  },
}));

export async function refreshToken(): Promise<string | null> {
  if (refreshPromise) return refreshPromise;
  const gen = refreshGeneration;
  refreshPromise = (async () => {
    try {
      const data = await apiClient.post<{ token: string }>('/api/v1/auth/refresh');
      if (gen !== refreshGeneration) {
        return null;
      }
      localStorage.setItem('auth_token', data.token);
      useAuthStore.setState({ token: data.token });
      return data.token;
    } catch {
      if (gen === refreshGeneration) {
        useAuthStore.getState().clearAuth();
      }
      return null;
    } finally {
      if (gen === refreshGeneration) {
        refreshPromise = null;
      }
    }
  })();
  return refreshPromise;
}

export async function ensureFreshToken(options: { force?: boolean; skewMs?: number } = {}): Promise<string | null> {
  const token = localStorage.getItem('auth_token');
  if (!token) return null;
  if (!options.force && !shouldRefreshToken(token, options.skewMs ?? DEFAULT_REFRESH_SKEW_MS)) {
    return token;
  }
  return refreshToken();
}

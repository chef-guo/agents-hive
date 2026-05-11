import { render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import { WeChatConnectionPanel } from '../WeChatConnectionPanel';

const qrcodeMock = vi.hoisted(() => ({
  toDataURL: vi.fn<(value: string, options?: unknown) => Promise<string>>(),
}));
const refresh = vi.fn();
const login = vi.fn();
const relogin = vi.fn();
const logout = vi.fn();

vi.mock('qrcode', () => ({
  default: {
    toDataURL: qrcodeMock.toDataURL,
  },
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (_key: string, fallback?: string) => fallback ?? _key,
  }),
}));

vi.mock('../../../hooks/useWechatConnection', () => ({
  useWechatConnection: () => ({
    status: {
      enabled: true,
      status: 'waiting_qr_scan',
      conversation_count: 0,
    },
    conversations: [],
    qrUrl: 'https://liteapp.weixin.qq.com/q/7GiQu1?qrcode=test&bot_type=3',
    lastEvent: null,
    loading: false,
    actionLoading: null,
    streamConnected: true,
    error: '',
    refresh,
    login,
    relogin,
    logout,
  }),
}));

describe('WeChatConnectionPanel', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders an SDK login URL as a generated QR image instead of using the URL as img src', async () => {
    const toDataURL = qrcodeMock.toDataURL;
    toDataURL.mockResolvedValue('data:image/png;base64,qr-image');

    render(
      <MemoryRouter>
        <WeChatConnectionPanel />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(toDataURL).toHaveBeenCalledWith(
        'https://liteapp.weixin.qq.com/q/7GiQu1?qrcode=test&bot_type=3',
        expect.objectContaining({ width: 240 }),
      );
    });

    const image = await screen.findByRole('img', { name: '微信登录二维码' });
    expect(image).toHaveAttribute('src', 'data:image/png;base64,qr-image');
    expect(image).not.toHaveAttribute(
      'src',
      'https://liteapp.weixin.qq.com/q/7GiQu1?qrcode=test&bot_type=3',
    );
  });
});

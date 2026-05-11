import { apiClient } from './client';
import { ensureFreshToken } from '../store/auth';

const BASE_URL = import.meta.env.VITE_API_BASE || '';

export type WeChatStatusValue =
  | 'disabled'
  | 'not_connected'
  | 'waiting_qr_scan'
  | 'scanned'
  | 'online'
  | 'recovering'
  | 'relogin_required'
  | 'offline'
  | 'error';

export interface WeChatConnectionStatus {
  enabled: boolean;
  status: WeChatStatusValue;
  owner_account_id?: string;
  display_name?: string;
  avatar_url?: string;
  conversation_count: number;
  last_connected_at?: string;
  error?: string;
}

export interface WeChatConversation {
  peer_wxid: string;
  peer_nickname?: string;
  peer_avatar_url?: string;
  chat_type: string;
  last_message_at?: string;
}

export interface WeChatEvent {
  type: string;
  status?: WeChatStatusValue;
  qr_url?: string;
  error?: string;
  created_at: string;
}

export interface WeChatConversationsResponse {
  conversations: WeChatConversation[];
}

export async function getWeChatStatus(): Promise<WeChatConnectionStatus> {
  return apiClient.get('/api/v1/wechat/status');
}

export async function loginWeChat(): Promise<WeChatConnectionStatus> {
  return apiClient.postLong('/api/v1/wechat/login');
}

export async function reloginWeChat(): Promise<WeChatConnectionStatus> {
  return apiClient.postLong('/api/v1/wechat/relogin');
}

export async function logoutWeChat(): Promise<void> {
  await apiClient.post('/api/v1/wechat/logout');
}

export async function listWeChatConversations(): Promise<WeChatConversation[]> {
  const res = await apiClient.get<WeChatConversationsResponse>('/api/v1/wechat/conversations');
  return res.conversations ?? [];
}

export async function streamWeChatEvents(
  onEvent: (event: WeChatEvent) => void,
  signal: AbortSignal,
): Promise<void> {
  const token = await ensureFreshToken();
  const headers: Record<string, string> = { Accept: 'text/event-stream' };
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }

  const res = await fetch(`${BASE_URL}/api/v1/wechat/events`, { headers, signal });
  if (!res.ok) {
    throw new Error(`wechat events stream failed: ${res.status}`);
  }
  if (!res.body) {
    throw new Error('wechat events stream has no body');
  }

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  while (!signal.aborted) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });

    let boundary = buffer.indexOf('\n\n');
    while (boundary >= 0) {
      const raw = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      const event = parseSSEEvent(raw);
      if (event) onEvent(event);
      boundary = buffer.indexOf('\n\n');
    }
  }
}

function parseSSEEvent(raw: string): WeChatEvent | null {
  const dataLines = raw
    .split('\n')
    .filter((line) => line.startsWith('data:'))
    .map((line) => line.slice(5).trimStart());
  if (dataLines.length === 0) return null;
  try {
    return JSON.parse(dataLines.join('\n')) as WeChatEvent;
  } catch {
    return null;
  }
}

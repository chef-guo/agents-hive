import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ChatLanding } from '../ChatLanding';
import { Chat } from '../Chat';
import { useAppStore } from '../../store/app';
import { useChatStore } from '../../store/chat';
import { useSessionStore } from '../../store/session';
import type { NodeClient } from '../../api/node-client';
import type { KBNamespace } from '../../types/api';

const navigateMock = vi.fn();
let locationState: unknown = null;
let routeSessionId = 'draft-session';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: string) => fallback ?? key,
  }),
}));

vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>();
  return {
    ...actual,
    useNavigate: () => navigateMock,
    useLocation: () => ({ state: locationState }),
    useParams: () => ({ id: routeSessionId }),
  };
});

vi.mock('../../components/chat/MessageList', () => ({
  MessageList: () => <div data-testid="message-list" />,
}));

vi.mock('../../components/todos/TodosList', () => ({
  TodosList: () => null,
}));

vi.mock('../../components/canvas/CanvasPanel', () => ({
  CanvasPanel: () => null,
}));

vi.mock('../../store/todos', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../store/todos')>();
  return {
    ...actual,
    shouldShowTodosPanel: () => false,
  };
});

const namespace: KBNamespace = {
  id: 'ns-a',
  name: '产品 FAQ',
  domain_id: 'support',
  owner_scope: 'user',
  owner_id: 'user-1',
  index_strategy: 'markdown_tree',
  thinning_enabled: false,
  thinning_token_threshold: 0,
  summary_token_threshold: 0,
  created_at: '2026-05-17T00:00:00Z',
  updated_at: '2026-05-17T00:00:00Z',
};

function createClient(overrides: Partial<NodeClient> = {}): NodeClient {
  return {
    listSessions: vi.fn().mockResolvedValue([]),
    createSession: vi.fn().mockResolvedValue({ session_id: 'draft-session', name: '新会话' }),
    listModels: vi.fn().mockResolvedValue({
      active: 'gpt-5.2',
      models: [{ name: 'gpt-5.2', model: 'gpt-5.2', is_active: true }],
    }),
    switchModel: vi.fn().mockResolvedValue(undefined),
    listKBNamespaces: vi.fn().mockResolvedValue({ namespaces: [namespace] }),
    getSessionKBBindings: vi.fn().mockResolvedValue({ bindings: [] }),
    setSessionKBBindings: vi.fn().mockResolvedValue({ bindings: [] }),
    deleteSessionKBBinding: vi.fn().mockResolvedValue({ bindings: [] }),
    updateSession: vi.fn().mockResolvedValue(undefined),
    getSession: vi.fn().mockResolvedValue({
      id: 'draft-session',
      name: '新会话',
      message_count: 0,
      total_tokens: 0,
      kb_domain_id: 'support',
      last_accessed: '2026-05-17T00:00:00Z',
      created: '2026-05-17T00:00:00Z',
      updated: '2026-05-17T00:00:00Z',
      tags: [],
      is_active: true,
    }),
    getMessages: vi.fn().mockResolvedValue([]),
    sendMessage: vi.fn().mockResolvedValue({ content: 'ok', completed: true }),
    ...overrides,
  } as unknown as NodeClient;
}

function resetStores(client: NodeClient) {
  useAppStore.setState({ nodeClient: client });
  useSessionStore.setState({
    sessions: [],
    currentSession: null,
    loading: false,
    error: null,
  });
  useChatStore.setState({
    messages: [],
    sending: false,
    streaming: false,
    streamingMessageId: null,
    agentStatus: null,
    error: null,
    currentSessionId: null,
    inlineApprovals: [],
    availableModels: [],
    activeModel: null,
    toolCallStatuses: {},
    toolCallStartTimes: {},
  });
}

describe('ChatLanding composer', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    locationState = null;
    routeSessionId = 'draft-session';
  });

  it('uses the full chat composer and carries first-message options into the session route', async () => {
    const client = createClient();
    resetStores(client);

    render(
      <MemoryRouter>
        <ChatLanding />
      </MemoryRouter>,
    );

    expect(await screen.findByTitle('chat.model')).toBeInTheDocument();
    expect(screen.getByTitle('chat.attachment')).toBeInTheDocument();
    expect(screen.getByTitle('chat.deepThinking')).toBeInTheDocument();
    expect(screen.getByLabelText('KB domain')).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('KB domain'), { target: { value: 'support' } });
    fireEvent.click(await screen.findByRole('button', { name: /选择知识库/ }));
    fireEvent.click(screen.getByText('产品 FAQ'));
    fireEvent.click(screen.getByTitle('chat.deepThinking'));
    fireEvent.change(screen.getByPlaceholderText('chat.inputPlaceholder'), { target: { value: '帮我查产品 FAQ' } });
    fireEvent.click(screen.getByRole('button', { name: '发送' }));

    await waitFor(() => {
      expect(navigateMock).toHaveBeenCalledWith('/sessions/draft-session', {
        state: {
          pendingMessage: '帮我查产品 FAQ',
          pendingOptions: { deepThinking: true },
          pendingKBDomainId: 'support',
        },
      });
    });
    expect(client.setSessionKBBindings).toHaveBeenCalledWith('draft-session', ['ns-a'], 'support');
  });

  it('creates a draft session before switching model from the landing composer', async () => {
    const client = createClient({
      listModels: vi.fn().mockResolvedValue({
        active: 'gpt-5.2',
        models: [
          { name: 'gpt-5.2', model: 'gpt-5.2', is_active: true },
          { name: 'o3-mini', model: 'o3-mini', is_active: false },
        ],
      }),
    });
    resetStores(client);

    render(
      <MemoryRouter>
        <ChatLanding />
      </MemoryRouter>,
    );

    fireEvent.click(await screen.findByTitle('chat.model'));
    const modelOption = screen.getAllByText('o3-mini')[0].closest('button');
    expect(modelOption).not.toBeNull();
    fireEvent.click(modelOption as HTMLElement);

    await waitFor(() => {
      expect(client.switchModel).toHaveBeenCalledWith('draft-session', 'o3-mini');
    });
  });

  it('sends pending first-message options from route state', async () => {
    const client = createClient();
    resetStores(client);
    locationState = {
      pendingMessage: '分析这个文件',
      pendingOptions: {
        deepThinking: true,
        attachments: [{
          filename: 'case.md',
          mime_type: 'text/markdown',
          data: 'Y2FzZQ==',
          size: 4,
        }],
      },
      pendingKBDomainId: 'support',
    };

    render(
      <MemoryRouter>
        <Chat />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(client.sendMessage).toHaveBeenCalledWith('draft-session', '分析这个文件', {
        deepThinking: true,
        attachments: [{
          filename: 'case.md',
          mime_type: 'text/markdown',
          data: 'Y2FzZQ==',
          size: 4,
        }],
        kbDomainId: 'support',
      });
    });
  });
});

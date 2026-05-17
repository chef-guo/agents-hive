import { beforeEach, describe, it, expect, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MessageList } from '../MessageList';
import { useChatStore } from '../../../store/chat';
import type { Message } from '../../../types/api';

const resolveAssetMock = vi.hoisted(() => vi.fn().mockResolvedValue({
  url: '/api/v1/assets/proxy?x=1',
  expires_in: 300,
  mime_type: 'image/png',
  size: 3,
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, defaultValue?: string) => {
      const map: Record<string, string> = {
        'tools.write_file': 'Write File',
        'tools.clickToExpand': 'Click to expand',
        'tools.clickToCollapse': 'Click to collapse',
        'tools.output': 'Output',
        'tools.input': 'Input',
        'tools.needsApproval': 'Needs approval',
        'tools.recoverable': 'Recoverable',
        'tools.truncated': '(truncated)',
        'chat.welcomeTitle': 'Welcome',
        'chat.emptyHint': 'Start chatting',
        'chat.regenerate': 'Regenerate',
        'nav.replay': 'Replay',
      };
      return map[key] ?? defaultValue ?? key;
    },
  }),
}));

vi.mock('../../../hooks/useHITLSubmit', () => ({
  useHITLSubmit: () => ({ submitApproval: vi.fn() }),
}));

vi.mock('../../../store/taskProgress', () => ({
  useTaskProgressStore: (selector: (state: { activeGroups: Map<string, unknown> }) => unknown) =>
    selector({ activeGroups: new Map() }),
}));

vi.mock('../../../api/node-client', () => ({
  LocalNodeClient: vi.fn().mockImplementation(() => ({
    resolveAsset: resolveAssetMock,
  })),
}));

describe('MessageList tool call rendering', () => {
  beforeEach(() => {
    resolveAssetMock.mockClear();
  });

  it('collapses successful tool calls into a compact diagnostics entry', () => {
    useChatStore.setState({
      inlineApprovals: [],
      toolCallStatuses: {},
      toolCallStartTimes: {},
    });

    const messages: Message[] = [
      {
        role: 'assistant',
        content: '',
        timestamp: '2026-05-04T01:00:00.000Z',
        tool_calls: [
          {
            id: 'call-write',
            name: 'write_file',
            arguments: '{"file_path":"/tmp/SKILL.md","content":"hello"}',
          },
          {
            id: 'call-write',
            name: 'write_file',
            arguments: '{"file_path":"/tmp/SKILL.md","content":"hello"}',
          },
        ],
      },
      {
        role: 'tool',
        content: '已写入 5 字节到 /tmp/SKILL.md',
        timestamp: '2026-05-04T01:00:01.000Z',
        tool_call_id: 'call-write',
        tool_name: 'write_file',
      },
    ];

    render(<MessageList messages={messages} sessionId="sess-1" />);

    expect(screen.getByText(/1 tool completed/)).toBeTruthy();
    expect(screen.getByText('Replay')).toBeTruthy();
    expect(screen.getByText('Trace')).toBeTruthy();
    expect(screen.getByText('Admin')).toBeTruthy();
    expect(document.querySelector('a[href="/sessions/sess-1/replay"]')).toBeTruthy();
    expect(document.querySelector('a[href="/sessions/sess-1/replay?trace=1"]')).toBeTruthy();
    expect(document.querySelector('a[href="/admin/quality-workbench"]')).toBeTruthy();
    expect(screen.queryByText('Completed')).toBeNull();
    expect(document.querySelectorAll('[data-slot="collapsible"]').length).toBe(0);
    expect(screen.queryByText(/已写入 5 字节到/)).toBeNull();
  });

  it('wires regenerate action on the last user message', () => {
    const onRegenerate = vi.fn();
    const messages: Message[] = [
      {
        role: 'user',
        content: 'old prompt',
        timestamp: '2026-05-04T01:00:00.000Z',
      },
      {
        role: 'assistant',
        content: 'old answer',
        timestamp: '2026-05-04T01:00:01.000Z',
      },
    ];

    render(<MessageList messages={messages} onRegenerate={onRegenerate} sessionId="sess-1" />);

    fireEvent.click(screen.getByRole('button', { name: 'Regenerate' }));

    expect(onRegenerate).toHaveBeenCalledTimes(1);
  });

  it('keeps failed tool calls expanded in chat while successful calls stay compact', () => {
    useChatStore.setState({
      inlineApprovals: [],
      toolCallStatuses: {},
      toolCallStartTimes: {},
    });

    const messages: Message[] = [
      {
        role: 'assistant',
        content: '',
        timestamp: '2026-05-04T01:00:00.000Z',
        tool_calls: [
          {
            id: 'call-ok',
            name: 'write_file',
            arguments: '{"file_path":"/tmp/SKILL.md","content":"hello"}',
          },
          {
            id: 'call-fail',
            name: 'write_file',
            arguments: '{"file_path":"/root/SKILL.md","content":"hello"}',
          },
        ],
      },
      {
        role: 'tool',
        content: '已写入 5 字节到 /tmp/SKILL.md',
        timestamp: '2026-05-04T01:00:01.000Z',
        tool_call_id: 'call-ok',
        tool_name: 'write_file',
      },
      {
        role: 'tool',
        content: 'permission denied',
        timestamp: '2026-05-04T01:00:02.000Z',
        tool_call_id: 'call-fail',
        tool_name: 'write_file',
        is_error: true,
      },
    ];

    render(<MessageList messages={messages} sessionId="sess-1" />);

    expect(screen.getByText(/1 tool completed/)).toBeTruthy();
    expect(screen.getByText('Error')).toBeTruthy();
    expect(screen.getAllByText('Write File').length).toBeGreaterThan(0);
    expect(document.querySelectorAll('[data-slot="collapsible"]').length).toBe(1);
    expect(screen.queryByText(/已写入 5 字节到/)).toBeNull();
  });

  it('keeps successful kb tool calls visible as KB result cards', async () => {
    useChatStore.setState({
      inlineApprovals: [],
      toolCallStatuses: {},
      toolCallStartTimes: {},
    });

    const messages: Message[] = [
      {
        role: 'assistant',
        content: '',
        timestamp: '2026-05-04T01:00:00.000Z',
        tool_calls: [
          {
            id: 'call-kb',
            name: 'kb.section.text',
            arguments: '{"doc_id":"doc-1","node_ids":["0001"]}',
          },
        ],
      },
      {
        role: 'tool',
        content: JSON.stringify({
          sections: [{ node_id: '0001', title: 'Refund Policy', text: 'policy' }],
          asset_refs: [{
            asset_uri: 'asset://kb/user/u1/ns/doc/hash.png',
            mime_type: 'image/png',
            content_hash: 'hash',
          }],
        }),
        timestamp: '2026-05-04T01:00:01.000Z',
        tool_call_id: 'call-kb',
        tool_name: 'kb.section.text',
      },
    ];

    render(<MessageList messages={messages} sessionId="sess-1" kbDomainId="support" />);

    expect(screen.getByText('Kb.Section.Text')).toBeTruthy();
    expect(screen.getByText(/1 节点/)).toBeTruthy();
    expect(screen.queryByText(/1 tool completed/)).toBeNull();
    await waitFor(() => expect(document.querySelector('a[href="/api/v1/assets/proxy?x=1"]')).toBeTruthy());
  });

  it('keeps kb metadata and structure tool calls visible instead of dropping them', () => {
    useChatStore.setState({
      inlineApprovals: [],
      toolCallStatuses: {},
      toolCallStartTimes: {},
    });

    const messages: Message[] = [
      {
        role: 'assistant',
        content: '',
        timestamp: '2026-05-04T01:00:00.000Z',
        tool_calls: [
          {
            id: 'call-meta',
            name: 'kb.doc.meta',
            arguments: '{}',
          },
          {
            id: 'call-structure',
            name: 'kb.doc.structure',
            arguments: '{"doc_id":"doc-1"}',
          },
        ],
      },
      {
        role: 'tool',
        content: JSON.stringify({
          documents: [{
            doc_id: 'doc-1',
            title: 'Refund Policy',
            version: 'v1',
            page_count: 8,
            node_count: 3,
          }],
        }),
        timestamp: '2026-05-04T01:00:01.000Z',
        tool_call_id: 'call-meta',
        tool_name: 'kb.doc.meta',
      },
      {
        role: 'tool',
        content: JSON.stringify({
          doc_id: 'doc-1',
          nodes: [{
            id: '0000',
            title: 'Refund Policy',
            node_path: '1',
            level: 1,
            token_count: 10,
            summary: '',
            prefix_summary: '',
            start_line: 1,
            end_line: 5,
            children: [{ id: '0001', title: 'Scope', node_path: '1.1' }],
          }],
        }),
        timestamp: '2026-05-04T01:00:02.000Z',
        tool_call_id: 'call-structure',
        tool_name: 'kb.doc.structure',
      },
    ];

    render(<MessageList messages={messages} sessionId="sess-1" />);

    expect(screen.getByText('Kb.Doc.Meta')).toBeTruthy();
    expect(screen.getByText('Refund Policy')).toBeTruthy();
    expect(screen.getByText(/1 文档/)).toBeTruthy();
    expect(screen.getByText('Kb.Doc.Structure')).toBeTruthy();
    expect(screen.getByText(/2 节点/)).toBeTruthy();
    expect(screen.queryByText(/2 tools completed/)).toBeNull();
  });

  it('resolves historical chat attachments with the current session boundary', async () => {
    useChatStore.setState({
      inlineApprovals: [],
      toolCallStatuses: {},
      toolCallStartTimes: {},
    });

    const messages: Message[] = [
      {
        role: 'user',
        content: 'see attachment',
        timestamp: '2026-05-04T01:00:00.000Z',
        attachments: [{
          filename: 'diagram.png',
          mime_type: 'image/png',
          size: 3,
          asset_uri: 'asset://chat/user/u1/session/sess-1/hash.png',
          content_hash: 'hash',
        }],
      },
    ];

    render(<MessageList messages={messages} sessionId="sess-1" />);

    await waitFor(() => expect(resolveAssetMock).toHaveBeenCalledWith(
      'asset://chat/user/u1/session/sess-1/hash.png',
      {
        purpose: 'chat_attachment',
        sessionId: 'sess-1',
      },
    ));
  });

  it('renders recoverable approval errors as non-terminal tool diagnostics', () => {
    useChatStore.setState({
      inlineApprovals: [],
      toolCallStatuses: {},
      toolCallStartTimes: {},
    });

    const messages: Message[] = [
      {
        role: 'assistant',
        content: '',
        timestamp: '2026-05-04T01:00:00.000Z',
        tool_calls: [
          {
            id: 'call-send',
            name: 'feishu_api',
            arguments: '{"action":"send_file","path":"/tmp/tool-policy-smoke.txt"}',
          },
        ],
      },
      {
        role: 'tool',
        content: '[可恢复工具调用错误: approval_channel_missing] 需要审批',
        timestamp: '2026-05-04T01:00:01.000Z',
        tool_call_id: 'call-send',
        tool_name: 'feishu_api',
        is_error: true,
        recoverable: true,
        terminal: false,
        error_kind: 'approval_channel_missing',
      },
    ];

    render(<MessageList messages={messages} sessionId="sess-1" />);

    expect(screen.getByText('Needs approval')).toBeTruthy();
    expect(screen.getByText('Awaiting Approval')).toBeTruthy();
  });

  it('shows the assistant avatar only once across a continuous assistant/tool turn', () => {
    useChatStore.setState({
      inlineApprovals: [],
      toolCallStatuses: {},
      toolCallStartTimes: {},
    });

    const messages: Message[] = [
      {
        role: 'user',
        content: 'Build a greeting skill',
        timestamp: '2026-05-04T01:00:00.000Z',
      },
      {
        role: 'assistant',
        content: '',
        timestamp: '2026-05-04T01:00:01.000Z',
        tool_calls: [
          {
            id: 'call-ls',
            name: 'ls',
            arguments: '{}',
          },
        ],
      },
      {
        role: 'tool',
        content: 'README.md\nfrontend',
        timestamp: '2026-05-04T01:00:02.000Z',
        tool_call_id: 'call-ls',
        tool_name: 'ls',
      },
      {
        role: 'assistant',
        content: 'I found the project files.',
        timestamp: '2026-05-04T01:00:03.000Z',
      },
    ];

    const { container } = render(<MessageList messages={messages} />);

    expect(container.querySelectorAll('.msg-container linearGradient[id^="claw-grad-"]').length).toBe(1);
  });

  it('keeps orphan tool result visible when no assistant tool_call references it', () => {
    useChatStore.setState({
      inlineApprovals: [],
      toolCallStatuses: {},
      toolCallStartTimes: {},
    });

    render(
      <MessageList
        messages={[
          {
            role: 'tool',
            content: '已写入 5 字节到 /tmp/SKILL.md',
            timestamp: '2026-05-04T01:00:01.000Z',
            tool_call_id: 'call-write',
            tool_name: 'write_file',
          },
        ]}
      />,
    );

    expect(screen.getByText(/已写入 5 字节到/)).toBeTruthy();
  });
});

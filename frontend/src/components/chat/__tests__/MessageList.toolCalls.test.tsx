import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MessageList } from '../MessageList';
import { useChatStore } from '../../../store/chat';
import type { Message } from '../../../types/api';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, defaultValue?: string) => {
      const map: Record<string, string> = {
        'tools.write_file': 'Write File',
        'tools.clickToExpand': 'Click to expand',
        'tools.clickToCollapse': 'Click to collapse',
        'tools.output': 'Output',
        'tools.input': 'Input',
        'tools.truncated': '(truncated)',
        'chat.welcomeTitle': 'Welcome',
        'chat.emptyHint': 'Start chatting',
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

describe('MessageList tool call rendering', () => {
  it('renders one combined tool card for matching assistant tool_call and tool result', () => {
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

    render(<MessageList messages={messages} />);

    expect(screen.getByText('Write File')).toBeTruthy();
    expect(screen.getAllByText('Completed')).toHaveLength(1);
    expect(document.querySelectorAll('[data-slot="collapsible"]').length).toBe(1);
    expect(screen.queryByText(/已写入 5 字节到/)).toBeNull();
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

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useChatStore } from '../chat';
import type { NodeClient } from '../../api/node-client';

function createClient(response: { content: string; completed: boolean }): NodeClient {
  return {
    sendMessage: vi.fn().mockResolvedValue(response),
  } as unknown as NodeClient;
}

function createDeferredClient() {
  let resolve: (response: { content: string; completed: boolean }) => void = () => {};
  const response = new Promise<{ content: string; completed: boolean }>((res) => {
    resolve = res;
  });
  return {
    client: {
      sendMessage: vi.fn().mockReturnValue(response),
    } as unknown as NodeClient,
    resolve,
  };
}

describe('chat store sendMessage fallback', () => {
  beforeEach(() => {
    useChatStore.setState({
      messages: [],
      sending: false,
      streaming: false,
      streamingMessageId: null,
      agentStatus: null,
      error: null,
      currentSessionId: null,
      inlineApprovals: [],
      toolCallStatuses: {},
      toolCallStartTimes: {},
    });
  });

  it('adds completed HTTP response when WebSocket did not deliver assistant message', async () => {
    const client = createClient({ content: '2', completed: true });

    await useChatStore.getState().sendMessage(client, 'session-1', '1+1等于几');

    const messages = useChatStore.getState().messages;
    expect(messages).toHaveLength(2);
    expect(messages[0]).toMatchObject({ role: 'user', content: '1+1等于几' });
    expect(messages[1]).toMatchObject({ role: 'assistant', content: '2' });
    expect(useChatStore.getState().sending).toBe(false);
    expect(useChatStore.getState().streaming).toBe(false);
  });

  it('does not duplicate HTTP response when WebSocket already added assistant message', async () => {
    const client = createClient({ content: '2', completed: true });
    const pending = useChatStore.getState().sendMessage(client, 'session-1', '1+1等于几');

    useChatStore.getState().addMessage({
      role: 'assistant',
      content: '2',
      timestamp: '2026-05-04T01:00:00.000Z',
    }, 'session-1');
    await pending;

    const assistantMessages = useChatStore.getState().messages.filter((m) => m.role === 'assistant');
    expect(assistantMessages).toHaveLength(1);
    expect(assistantMessages[0].content).toBe('2');
  });

  it('does not let previous assistant history block current HTTP fallback', async () => {
    useChatStore.setState({
      messages: [
        { role: 'user', content: '上一轮问题', timestamp: '2026-05-04T01:00:00.000Z' },
        { role: 'assistant', content: '上一轮回答', timestamp: '2026-05-04T01:00:01.000Z' },
      ],
      currentSessionId: 'session-1',
    });
    const client = createClient({ content: '2', completed: true });

    await useChatStore.getState().sendMessage(client, 'session-1', '1+1等于几');

    const messages = useChatStore.getState().messages;
    expect(messages).toHaveLength(4);
    expect(messages.at(-2)).toMatchObject({ role: 'user', content: '1+1等于几' });
    expect(messages.at(-1)).toMatchObject({ role: 'assistant', content: '2' });
  });

  it('replaces streaming placeholder with completed HTTP response when final WebSocket frame is missing', async () => {
    const { client, resolve } = createDeferredClient();
    const pending = useChatStore.getState().sendMessage(client, 'session-1', '1+1等于几');

    useChatStore.getState().ensureAssistantMessage();
    useChatStore.getState().updateLastAssistant('处理中');
    resolve({ content: '2', completed: true });
    await pending;

    const assistantMessages = useChatStore.getState().messages.filter((m) => m.role === 'assistant');
    expect(assistantMessages).toHaveLength(1);
    expect(assistantMessages[0].content).toBe('2');
    expect(assistantMessages[0].timestamp).not.toMatch(/^stream-/);
    expect(useChatStore.getState().streaming).toBe(false);
    expect(useChatStore.getState().streamingMessageId).toBeNull();
  });
});

describe('chat store message normalization', () => {
  beforeEach(() => {
    useChatStore.setState({
      messages: [],
      sending: false,
      streaming: false,
      streamingMessageId: null,
      agentStatus: null,
      error: null,
      currentSessionId: 'session-1',
      inlineApprovals: [],
      toolCallStatuses: {},
      toolCallStartTimes: {},
    });
  });

  it('keeps backend/history order instead of sorting by timestamp', () => {
    useChatStore.getState().setMessages([
      { role: 'user', content: '先发送的问题', timestamp: '2026-05-04T01:00:10.000Z' },
      { role: 'assistant', content: '后产生的回答', timestamp: '2026-05-04T01:00:05.000Z' },
    ]);

    expect(useChatStore.getState().messages.map((m) => m.content)).toEqual([
      '先发送的问题',
      '后产生的回答',
    ]);
  });

  it('merges duplicate assistant tool-call preview and final frames by tool_call id', () => {
    useChatStore.getState().addMessage({
      role: 'assistant',
      content: '',
      timestamp: '2026-05-04T01:00:00.000Z',
      tool_call_preview: true,
      tool_calls: [{
        id: 'call-write',
        name: 'write_file',
        arguments: '{"content":"---\\nname: hello',
      }],
    });
    useChatStore.getState().addMessage({
      role: 'assistant',
      content: '',
      timestamp: '2026-05-04T01:00:20.000Z',
      tool_calls: [{
        id: 'call-write',
        name: 'write_file',
        arguments: '{"content":"---\\nname: hello-greet\\nversion: \\"1.0.0\\"","file_path":"/tmp/SKILL.md"}',
      }],
    });

    const messages = useChatStore.getState().messages;
    expect(messages).toHaveLength(1);
    expect(messages[0].timestamp).toBe('2026-05-04T01:00:20.000Z');
    expect(messages[0].tool_calls).toHaveLength(1);
    expect(messages[0].tool_calls?.[0].arguments).toContain('/tmp/SKILL.md');
  });

  it('keeps assistant tool call and matching tool result as separate linked messages', () => {
    useChatStore.getState().setMessages([
      {
        role: 'assistant',
        content: '',
        timestamp: '2026-05-04T01:00:00.000Z',
        tool_calls: [{
          id: 'call-write',
          name: 'write_file',
          arguments: '{"file_path":"/tmp/SKILL.md","content":"hello"}',
        }],
      },
      {
        role: 'tool',
        content: '已写入 5 字节到 /tmp/SKILL.md',
        timestamp: '2026-05-04T01:00:01.000Z',
        tool_call_id: 'call-write',
        tool_name: 'write_file',
      },
    ]);

    const messages = useChatStore.getState().messages;
    expect(messages).toHaveLength(2);
    expect(messages[0].role).toBe('assistant');
    expect(messages[1]).toMatchObject({ role: 'tool', tool_call_id: 'call-write' });
  });

  it('merges duplicate tool results by tool_call_id', () => {
    useChatStore.getState().addMessage({
      role: 'tool',
      content: '写入中',
      timestamp: '2026-05-04T01:00:00.000Z',
      tool_call_id: 'call-write',
      tool_name: 'write_file',
    });
    useChatStore.getState().addMessage({
      role: 'tool',
      content: '已写入 1194 字节到 /tmp/SKILL.md',
      timestamp: '2026-05-04T01:00:01.000Z',
      tool_call_id: 'call-write',
      tool_name: 'write_file',
    });

    const toolMessages = useChatStore.getState().messages.filter((m) => m.role === 'tool');
    expect(toolMessages).toHaveLength(1);
    expect(toolMessages[0].content).toBe('已写入 1194 字节到 /tmp/SKILL.md');
    expect(toolMessages[0].timestamp).toBe('2026-05-04T01:00:00.000Z');
  });
});

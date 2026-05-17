import { describe, expect, it } from 'vitest';
import { messagesAfterRegenerateSuccess, messagesForRegenerateStart } from '../chatRegenerate';
import type { Message } from '../../types/api';

describe('chat regenerate helpers', () => {
  const messages: Message[] = [
    { role: 'user', content: 'prompt', timestamp: '2026-05-17T00:00:00.000Z' },
    { role: 'assistant', content: 'old answer', timestamp: '2026-05-17T00:00:01.000Z' },
    { role: 'tool', content: 'old tool result', timestamp: '2026-05-17T00:00:02.000Z', tool_call_id: 'call-1' },
  ];

  it('keeps the last user message and removes old generated messages during pending regenerate', () => {
    expect(messagesForRegenerateStart(messages)).toEqual({
      messages: [messages[0]],
      lastUserMsgIdx: 0,
    });
  });

  it('appends HTTP fallback response when websocket did not add a final assistant message', () => {
    const pending = messagesForRegenerateStart(messages);
    const next = messagesAfterRegenerateSuccess(pending.messages, pending.lastUserMsgIdx, {
      content: 'new answer',
      completed: true,
    });

    expect(next).toHaveLength(2);
    expect(next[0]).toEqual(messages[0]);
    expect(next[1]).toMatchObject({ role: 'assistant', content: 'new answer' });
  });

  it('does not duplicate the assistant message when websocket already delivered it', () => {
    const wsMessages: Message[] = [
      messages[0],
      { role: 'assistant', content: 'new answer from ws', timestamp: '2026-05-17T00:00:03.000Z' },
    ];

    expect(messagesAfterRegenerateSuccess(wsMessages, 0, {
      content: 'new answer from http',
      completed: true,
    })).toEqual(wsMessages);
  });
});

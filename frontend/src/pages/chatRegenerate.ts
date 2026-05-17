import type { Message, SendMessageResponse } from '../types/api';
import { rfc3339Now } from '../utils/date';

export function messagesForRegenerateStart(messages: Message[]) {
  const lastUserMsgIdx = [...messages].map((m, i) => ({ role: m.role, i })).reverse().find(m => m.role === 'user')?.i;
  if (lastUserMsgIdx === undefined) {
    return { messages, lastUserMsgIdx };
  }
  return {
    messages: messages.slice(0, lastUserMsgIdx + 1),
    lastUserMsgIdx,
  };
}

export function messagesAfterRegenerateSuccess(messages: Message[], lastUserMsgIdx: number | undefined, resp: SendMessageResponse): Message[] {
  if (!resp?.content || !resp.completed) {
    return messages;
  }
  const hasFinalAssistantAfterUser = messages.some((msg, index) => {
    const ts = msg.timestamp || '';
    return lastUserMsgIdx !== undefined
      && index > lastUserMsgIdx
      && msg.role === 'assistant'
      && !msg.is_error
      && !ts.startsWith('stream-')
      && msg.content.trim().length > 0;
  });
  if (hasFinalAssistantAfterUser) {
    return messages;
  }
  return [
    ...messages.filter((msg) => !(msg.role === 'assistant' && (msg.timestamp || '').startsWith('stream-'))),
    {
      role: 'assistant',
      content: resp.content,
      timestamp: rfc3339Now(),
    },
  ];
}

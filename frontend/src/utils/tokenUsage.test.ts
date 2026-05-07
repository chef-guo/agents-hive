import { describe, expect, it } from 'vitest';
import type { Message } from '../types/api';
import { calculateMessageTotalTokens } from './tokenUsage';

describe('calculateMessageTotalTokens', () => {
  it('sums input and output tokens for session header totals', () => {
    const messages: Message[] = [
      { role: 'user', content: 'hello' },
      { role: 'assistant', content: 'world', usage: { input_tokens: 120, output_tokens: 30 } },
      { role: 'assistant', content: 'again', usage: { input_tokens: 80, output_tokens: 20 } },
    ];

    expect(calculateMessageTotalTokens(messages)).toBe(250);
  });
});

import type { Message } from '../types/api';

export function calculateMessageTotalTokens(messages: Message[]): number {
  return messages.reduce((sum, message) => {
    const usage = message.usage;
    if (!usage) return sum;
    return sum + usage.input_tokens + usage.output_tokens;
  }, 0);
}

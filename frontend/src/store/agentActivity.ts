// frontend/src/store/agentActivity.ts
// 精简版：仅保留 Sidebar SessionStatusDot 所需的 sessionStatus 状态
import { create } from 'zustand';

interface AgentActivityState {
  // 当前状态（用于侧边栏圆点）
  sessionStatus: Record<string, 'idle' | 'running' | 'error'>;

  onAgentStatus: (sessionId: string, status: string) => void;
  clearActivities: (sessionId: string) => void;
}

export const useAgentActivityStore = create<AgentActivityState>((set) => ({
  sessionStatus: {},

  onAgentStatus: (sessionId, status) => {
    set((s) => {
      if (status === 'thinking' || status === 'tool_calling') {
        return { sessionStatus: { ...s.sessionStatus, [sessionId]: 'running' } };
      }
      if (status === 'completed' || status === 'paused') {
		return { sessionStatus: { ...s.sessionStatus, [sessionId]: 'idle' } };
	}
      if (status === 'error') {
        return { sessionStatus: { ...s.sessionStatus, [sessionId]: 'error' } };
      }
      return {};
    });
  },

  clearActivities: (sessionId) => {
    set((s) => {
      const status = { ...s.sessionStatus };
      delete status[sessionId];
      return { sessionStatus: status };
    });
  },
}));

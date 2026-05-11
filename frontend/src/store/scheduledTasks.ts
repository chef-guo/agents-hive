import { create } from 'zustand';
import type { NodeClient } from '../api/node-client';
import type { ScheduledTask, ScheduledTaskRun, ScheduledTaskUpsertRequest } from '../types/api';
import { useToastStore } from './toast';

interface ScheduledTasksState {
  tasks: ScheduledTask[];
  runsByTaskId: Record<string, ScheduledTaskRun[]>;
  loading: boolean;
  runsLoadingByTaskId: Record<string, boolean>;
  error: string | null;
  partialWarning: string | null;
  runningNowTaskId: string | null;
  loadTasks: (client: NodeClient) => Promise<void>;
  createTask: (client: NodeClient, body: ScheduledTaskUpsertRequest) => Promise<ScheduledTask>;
  updateTask: (client: NodeClient, id: string, body: ScheduledTaskUpsertRequest) => Promise<ScheduledTask>;
  deleteTask: (client: NodeClient, id: string) => Promise<void>;
  toggleTask: (client: NodeClient, id: string, enabled: boolean) => Promise<void>;
  runNow: (client: NodeClient, id: string) => Promise<ScheduledTaskRun>;
  loadRuns: (client: NodeClient, id: string, limit?: number) => Promise<ScheduledTaskRun[]>;
  clearError: () => void;
}

function errorMessage(e: unknown, fallback: string): string {
  return e instanceof Error ? e.message : fallback;
}

function upsertTask(tasks: ScheduledTask[], task: ScheduledTask): ScheduledTask[] {
  const existing = tasks.some((item) => item.id === task.id);
  if (!existing) return [task, ...tasks];
  return tasks.map((item) => item.id === task.id ? task : item);
}

function listOrEmpty<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}

export const useScheduledTasksStore = create<ScheduledTasksState>((set, get) => ({
  tasks: [],
  runsByTaskId: {},
  loading: false,
  runsLoadingByTaskId: {},
  error: null,
  partialWarning: null,
  runningNowTaskId: null,

  loadTasks: async (client) => {
    set({ loading: true, error: null, partialWarning: null });
    try {
      const tasks = await client.listScheduledTasks();
      set({ tasks: listOrEmpty(tasks), loading: false });
    } catch (e) {
      set({
        loading: false,
        error: errorMessage(e, '加载定时任务失败'),
      });
    }
  },

  createTask: async (client, body) => {
    const task = await client.createScheduledTask(body);
    set((state) => ({ tasks: upsertTask(state.tasks, task), error: null }));
    useToastStore.getState().addToast('success', '定时任务已创建');
    return task;
  },

  updateTask: async (client, id, body) => {
    const task = await client.updateScheduledTask(id, body);
    set((state) => ({ tasks: upsertTask(state.tasks, task), error: null }));
    useToastStore.getState().addToast('success', '定时任务已更新');
    return task;
  },

  deleteTask: async (client, id) => {
    await client.deleteScheduledTask(id);
    set((state) => {
      const nextRuns = { ...state.runsByTaskId };
      delete nextRuns[id];
      return {
        tasks: state.tasks.filter((task) => task.id !== id),
        runsByTaskId: nextRuns,
        error: null,
      };
    });
    useToastStore.getState().addToast('success', '定时任务已删除');
  },

  toggleTask: async (client, id, enabled) => {
    const previous = get().tasks.find((task) => task.id === id);
    if (!previous) return;
    set((state) => ({
      tasks: state.tasks.map((task) => task.id === id ? { ...task, enabled } : task),
      error: null,
    }));
    try {
      const task = await client.toggleScheduledTask(id, enabled);
      set((state) => ({ tasks: upsertTask(state.tasks, task) }));
    } catch (e) {
      set((state) => ({
        tasks: state.tasks.map((task) => task.id === id ? previous : task),
        error: errorMessage(e, '更新定时任务状态失败'),
      }));
      useToastStore.getState().addToast('error', '更新定时任务状态失败');
      throw e;
    }
  },

  runNow: async (client, id) => {
    set({ runningNowTaskId: id, error: null });
    try {
      const run = await client.runScheduledTaskNow(id);
      set((state) => ({
        runningNowTaskId: null,
        runsByTaskId: {
          ...state.runsByTaskId,
          [id]: [run, ...(state.runsByTaskId[id] ?? [])].slice(0, 20),
        },
      }));
      useToastStore.getState().addToast('success', '已触发立即运行');
      return run;
    } catch (e) {
      set({
        runningNowTaskId: null,
        error: errorMessage(e, '立即运行失败'),
      });
      useToastStore.getState().addToast('error', '立即运行失败');
      throw e;
    }
  },

  loadRuns: async (client, id, limit = 20) => {
    set((state) => ({
      runsLoadingByTaskId: { ...state.runsLoadingByTaskId, [id]: true },
      partialWarning: null,
    }));
    try {
      const runs = await client.listScheduledTaskRuns(id, limit);
      const safeRuns = listOrEmpty(runs);
      set((state) => ({
        runsByTaskId: { ...state.runsByTaskId, [id]: safeRuns },
        runsLoadingByTaskId: { ...state.runsLoadingByTaskId, [id]: false },
      }));
      return safeRuns;
    } catch (e) {
      const message = errorMessage(e, '加载运行记录失败');
      set((state) => ({
        runsLoadingByTaskId: { ...state.runsLoadingByTaskId, [id]: false },
        partialWarning: message,
      }));
      return [];
    }
  },

  clearError: () => set({ error: null }),
}));

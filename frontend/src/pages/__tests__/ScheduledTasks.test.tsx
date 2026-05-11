import { render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ScheduledTasks } from '../ScheduledTasks';
import { useScheduledTasksStore } from '../../store/scheduledTasks';

const mockClient = {
  listScheduledTasks: vi.fn(),
  createScheduledTask: vi.fn(),
  updateScheduledTask: vi.fn(),
  deleteScheduledTask: vi.fn(),
  toggleScheduledTask: vi.fn(),
  runScheduledTaskNow: vi.fn(),
  listScheduledTaskRuns: vi.fn(),
};

vi.mock('../../hooks/useNodeClient', () => ({
  useNodeClient: () => mockClient,
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    i18n: { language: undefined },
    t: (_key: string, fallback?: string) => fallback ?? _key,
  }),
}));

describe('ScheduledTasks page', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useScheduledTasksStore.setState({
      tasks: [],
      runsByTaskId: {},
      loading: false,
      runsLoadingByTaskId: {},
      error: null,
      partialWarning: null,
      runningNowTaskId: null,
    });
  });

  it('renders the empty state after loading tasks', async () => {
    mockClient.listScheduledTasks.mockResolvedValue([]);

    render(<ScheduledTasks />);

    await waitFor(() => {
      expect(screen.getByText('暂无定时任务')).toBeInTheDocument();
    });
  });

  it('treats a null task list response as an empty list', async () => {
    mockClient.listScheduledTasks.mockResolvedValue(null);

    render(<ScheduledTasks />);

    await waitFor(() => {
      expect(screen.getByText('暂无定时任务')).toBeInTheDocument();
    });
  });

  it('renders a cron task without crashing', async () => {
    mockClient.listScheduledTasks.mockResolvedValue([
      {
        id: 'task-1',
        name: '每日巡检',
        description: '每天检查一次',
        target_type: 'session',
        target_config: {},
        prompt: 'run check',
        cron_expr: '0 9 * * *',
        timezone: 'Asia/Shanghai',
        enabled: true,
        created_by: 'u1',
        created_at: '2026-05-11T00:00:00Z',
        updated_at: '2026-05-11T00:00:00Z',
      },
    ]);

    render(<ScheduledTasks />);

    await waitFor(() => {
      expect(screen.getByText('每日巡检')).toBeInTheDocument();
    });
    expect(screen.getByText('0 9 * * *')).toBeInTheDocument();
  });
});

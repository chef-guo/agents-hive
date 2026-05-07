import { useTranslation } from 'react-i18next';
import { Tool, ToolContent, ToolHeader } from '@/components/ai-elements/tool';
import { useChatStore } from '../../store/chat';
import { getToolDisplayName } from '../../utils/toolName';
import { ToolInvocationChip } from './ToolInvocationChip';
import { ToolExecutionBlock } from './ToolExecutionBlock';
import { TodoToolResultCard } from './TodoToolResultCard';
import { isTodoWriteTool, parseTodoToolSnapshot } from './todoToolSnapshot';

type HiveStatus = 'running' | 'success' | 'error';
type AiToolState =
  | 'input-streaming'
  | 'input-available'
  | 'output-available'
  | 'output-error';

// Hive 三态 → AI Elements Tool state 映射。
// running → input-available（"Running" 文案 + 脉冲 ClockIcon）
// success → output-available（"Completed" + CheckCircle）
// error   → output-error（"Error" + XCircle）
const HIVE_TO_AI: Record<HiveStatus, AiToolState> = {
  running: 'input-available',
  success: 'output-available',
  error: 'output-error',
};

interface ToolAdapterProps {
  id: string;
  name: string;
  args: string;
  result?: string;
  hasError: boolean;
}

/**
 * <Tool> 外框 + 保留自研 chip/block 作为 content slot 的适配层。
 *
 * 设计约束（见 openspec/changes/chat-ui-migrate-ai-elements/design.md）：
 *   - toolCallStatuses store 订阅路径不动（与 ToolExecutionBlock 各自订阅，收敛到同一 live 源）
 *   - chip/block 本身不删，保留作为业务壳；未来 AI Elements 提供等效能力再评估
 *   - running / error 态默认展开（用户需要看到调用上下文 / 错误详情）
 *   - success 态默认折叠（完成后收起省空间，点开查看输入输出）
 */
export function ToolAdapter({ id, name, args, result, hasError }: ToolAdapterProps) {
  const { t } = useTranslation();
  const liveStatus = useChatStore((s) => s.toolCallStatuses?.[id]);

  const resolvedStatus: HiveStatus = hasError
    ? 'error'
    : liveStatus?.status ?? 'success';

  const aiState = HIVE_TO_AI[resolvedStatus];
  const isRunning = resolvedStatus === 'running';
  const displayName = getToolDisplayName(name, t);

  if (!hasError && resolvedStatus === 'success' && isTodoWriteTool(name) && parseTodoToolSnapshot(result)) {
    return <TodoToolResultCard result={result} />;
  }

  return (
    <Tool defaultOpen={resolvedStatus !== 'success'}>
      <ToolHeader type="dynamic-tool" toolName={displayName} state={aiState} />
      <ToolContent>
        {isRunning ? (
          <ToolInvocationChip name={name} status="running" />
        ) : (
          <ToolExecutionBlock
            id={id}
            name={name}
            args={args}
            result={result}
            status={resolvedStatus}
          />
        )}
      </ToolContent>
    </Tool>
  );
}

export default ToolAdapter;

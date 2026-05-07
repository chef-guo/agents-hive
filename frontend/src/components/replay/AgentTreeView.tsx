import type { AgentTraceNode } from '../../types/api';

interface AgentTreeViewProps {
  nodes?: AgentTraceNode[];
}

export function AgentTreeView({ nodes = [] }: AgentTreeViewProps) {
  const roots = sortAgentNodes(nodes);
  if (roots.length === 0) {
    return (
      <div style={{ padding: 12, color: 'var(--text-secondary, #6C6C70)', fontSize: 12, fontFamily: 'DM Sans, sans-serif' }}>
        暂无 agent trace tree
      </div>
    );
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, padding: 12, fontFamily: 'DM Sans, sans-serif' }}>
      <div style={{ fontSize: 12, fontWeight: 700, color: 'var(--text-primary, #1C1C1E)' }}>
        Agent Tree
      </div>
      {roots.map((node) => (
        <AgentTreeBranch key={node.trace_id} node={node} />
      ))}
    </div>
  );
}

function AgentTreeBranch({ node }: { node: AgentTraceNode }) {
  const children = sortAgentNodes(node.children || []);
  return (
    <div style={{
      border: '1px solid var(--border, rgba(0,0,0,0.08))',
      borderRadius: 8,
      padding: 8,
      background: 'var(--bg-card, #fff)',
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--text-primary, #1C1C1E)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {node.agent_id || node.trace_id}
        </div>
        {node.status && (
          <span style={{
            marginLeft: 'auto',
            fontSize: 11,
            color: statusColor(node.status),
            background: statusBg(node.status),
            borderRadius: 999,
            padding: '1px 6px',
            flexShrink: 0,
          }}>
            {node.status}
          </span>
        )}
      </div>
      <div style={{ marginTop: 2, fontFamily: 'JetBrains Mono, monospace', fontSize: 11, color: 'var(--text-secondary, #6C6C70)', wordBreak: 'break-all' }}>
        {node.type ? `${node.type} · ` : ''}
        {node.trace_id}
      </div>
      {children.length > 0 && (
        <div style={{ marginTop: 8, paddingLeft: 10, borderLeft: '1px solid var(--border, rgba(0,0,0,0.08))', display: 'flex', flexDirection: 'column', gap: 8 }}>
          {children.map((child) => (
            <AgentTreeBranch key={child.trace_id} node={child} />
          ))}
        </div>
      )}
    </div>
  );
}

function sortAgentNodes(nodes: AgentTraceNode[]): AgentTraceNode[] {
  return [...nodes].sort((a, b) => a.trace_id.localeCompare(b.trace_id));
}

function statusColor(status: string): string {
  if (status === 'fail' || status === 'failed' || status === 'error') return '#DC2626';
  if (status === 'blocked' || status === 'warn') return '#D97706';
  return '#059669';
}

function statusBg(status: string): string {
  if (status === 'fail' || status === 'failed' || status === 'error') return '#FEF2F2';
  if (status === 'blocked' || status === 'warn') return '#FFFBEB';
  return '#ECFDF5';
}

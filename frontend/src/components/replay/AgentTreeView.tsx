interface AgentTraceEdge {
  parent_trace_id?: string;
  child_trace_id?: string;
  agent_id?: string;
  agent_type?: string;
}

interface AgentTreeNode {
  trace_id: string;
  agent_id?: string;
  agent_type?: string;
  children: AgentTreeNode[];
}

function buildAgentTree(edges: AgentTraceEdge[]): AgentTreeNode[] {
  const nodes = new Map<string, AgentTreeNode>();
  const childTraceIds = new Set<string>();

  const nodeFor = (traceId: string): AgentTreeNode => {
    const existing = nodes.get(traceId);
    if (existing) {
      return existing;
    }
    const node: AgentTreeNode = { trace_id: traceId, children: [] };
    nodes.set(traceId, node);
    return node;
  };

  for (const edge of edges) {
    if (!edge.parent_trace_id || !edge.child_trace_id) {
      continue;
    }
    const parent = nodeFor(edge.parent_trace_id);
    const child = nodeFor(edge.child_trace_id);
    child.agent_id = edge.agent_id;
    child.agent_type = edge.agent_type;
    parent.children.push(child);
    childTraceIds.add(edge.child_trace_id);
  }

  const sortNode = (node: AgentTreeNode): AgentTreeNode => {
    node.children.sort((a, b) => a.trace_id.localeCompare(b.trace_id));
    node.children.forEach(sortNode);
    return node;
  };

  return Array.from(nodes.values())
    .filter((node) => !childTraceIds.has(node.trace_id))
    .sort((a, b) => a.trace_id.localeCompare(b.trace_id))
    .map(sortNode);
}

interface AgentTreeViewProps {
  edges: AgentTraceEdge[];
}

export function AgentTreeView({ edges }: AgentTreeViewProps) {
  const roots = buildAgentTree(edges);
  if (roots.length === 0) {
    return <div className="text-sm text-muted-foreground">No agent trace tree</div>;
  }
  return (
    <div className="space-y-2">
      {roots.map((node) => (
        <AgentTreeBranch key={node.trace_id} node={node} />
      ))}
    </div>
  );
}

function AgentTreeBranch({ node }: { node: AgentTreeNode }) {
  return (
    <div className="rounded-md border border-border/70 p-2">
      <div className="text-sm font-medium">{node.agent_id || node.trace_id}</div>
      <div className="text-xs text-muted-foreground">
        {node.agent_type ? `${node.agent_type} · ` : ''}
        {node.trace_id}
      </div>
      {node.children.length > 0 && (
        <div className="mt-2 space-y-2 border-l border-border/70 pl-3">
          {node.children.map((child) => (
            <AgentTreeBranch key={child.trace_id} node={child} />
          ))}
        </div>
      )}
    </div>
  );
}

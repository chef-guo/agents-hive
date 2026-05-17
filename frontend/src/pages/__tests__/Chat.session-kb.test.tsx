import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { SessionKBBar } from '../../components/chat/SessionKBBar';
import { buildSessionKBNamespaceIDs } from '../chatKBUtils';
import type { KBBinding, KBNamespace } from '../../types/api';

const namespaces: KBNamespace[] = [
  namespace('ns-bound', '已绑定知识库'),
  namespace('ns-a', '退款政策'),
  namespace('ns-b', '产品 FAQ'),
];

const bindings: KBBinding[] = [
  binding('binding-1', 'ns-bound'),
];

describe('SessionKBBar', () => {
  it('supports selecting multiple knowledge bases before binding', () => {
    const onChange = vi.fn();

    const { rerender } = render(
      <SessionKBBar
        namespaces={namespaces}
        bindings={bindings}
        value={[]}
        domainId="support"
        busy={false}
        onChange={onChange}
        onDomainChange={vi.fn()}
        onReload={vi.fn()}
        onAdd={vi.fn()}
        onRemove={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole('button', { name: /选择知识库/ }));
    fireEvent.click(screen.getByText('退款政策'));
    expect(onChange).toHaveBeenLastCalledWith(['ns-a']);

    rerender(
      <SessionKBBar
        namespaces={namespaces}
        bindings={bindings}
        value={['ns-a']}
        domainId="support"
        busy={false}
        onChange={onChange}
        onDomainChange={vi.fn()}
        onReload={vi.fn()}
        onAdd={vi.fn()}
        onRemove={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByText('产品 FAQ'));
    expect(onChange).toHaveBeenLastCalledWith(['ns-a', 'ns-b']);
  });
});

describe('buildSessionKBNamespaceIDs', () => {
  it('merges existing bindings and selected namespaces without duplicates', () => {
    expect(buildSessionKBNamespaceIDs(bindings, ['ns-a', 'ns-bound', 'ns-b'])).toEqual([
      'ns-bound',
      'ns-a',
      'ns-b',
    ]);
  });
});

function namespace(id: string, name: string): KBNamespace {
  return {
    id,
    name,
    domain_id: 'support',
    owner_scope: 'user',
    owner_id: 'user-1',
    index_strategy: 'markdown_tree',
    thinning_enabled: false,
    thinning_token_threshold: 0,
    summary_token_threshold: 0,
    created_at: '2026-05-17T00:00:00Z',
    updated_at: '2026-05-17T00:00:00Z',
  };
}

function binding(id: string, namespaceID: string): KBBinding {
  return {
    id,
    namespace_id: namespaceID,
    domain_id: 'support',
    binding_type: 'session',
    binding_target: 'session-1',
    enabled: true,
    effective_at: '2026-05-17T00:00:00Z',
  };
}

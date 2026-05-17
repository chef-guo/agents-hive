import type { KBBinding } from '../types/api';

export function buildSessionKBNamespaceIDs(bindings: KBBinding[], selectedNamespaceIDs: string[]) {
  return Array.from(new Set([
    ...bindings.map((binding) => binding.namespace_id),
    ...selectedNamespaceIDs.filter(Boolean),
  ]));
}

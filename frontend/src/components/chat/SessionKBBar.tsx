import { useEffect, useRef, useState } from 'react';
import { BookOpenText, Check, ChevronDown, Plus, X } from 'lucide-react';
import type { KBBinding, KBNamespace } from '../../types/api';

function toggleKBNamespaceSelection(current: string[], namespaceID: string) {
  if (current.includes(namespaceID)) {
    return current.filter((item) => item !== namespaceID);
  }
  return [...current, namespaceID];
}

export function SessionKBBar(props: {
  namespaces: KBNamespace[];
  bindings: KBBinding[];
  value: string[];
  domainId: string;
  busy: boolean;
  onChange: (value: string[]) => void;
  onDomainChange: (value: string) => void;
  onReload: () => void;
  onAdd: () => void;
  onRemove: (namespaceId: string) => void;
}) {
  const pickerRef = useRef<HTMLDivElement>(null);
  const [pickerOpen, setPickerOpen] = useState(false);
  const boundNamespaceIDs = new Set(props.bindings.map((binding) => binding.namespace_id));
  const available = props.namespaces.filter((namespace) => !boundNamespaceIDs.has(namespace.id));
  const nameByID = new Map(props.namespaces.map((namespace) => [namespace.id, namespace.name]));
  const selectedCount = props.value.length;

  useEffect(() => {
    const handleClick = (event: MouseEvent) => {
      if (pickerRef.current && !pickerRef.current.contains(event.target as Node)) {
        setPickerOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClick);
    return () => document.removeEventListener('mousedown', handleClick);
  }, []);

  return (
    <div className="shrink-0 pt-2">
      <div className="mx-auto max-w-4xl px-4">
        <div className="flex flex-wrap items-center gap-2 rounded-2xl border border-[var(--border-color)] bg-[var(--bg-card)] px-3 py-2 text-xs shadow-sm shadow-black/[0.04] dark:shadow-black/[0.2]">
          <span className="inline-flex items-center gap-1 text-[var(--text-secondary)]">
            <BookOpenText className="h-3.5 w-3.5" />
            KB
          </span>
          <input
            value={props.domainId}
            onChange={(e) => props.onDomainChange(e.target.value)}
            onBlur={props.onReload}
            disabled={props.busy}
            className="w-28 rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] px-2 py-1 font-mono text-[11px] text-[var(--text-primary)] disabled:opacity-50"
            aria-label="KB domain"
          />
          {props.bindings.length === 0 ? (
            <span className="text-[var(--text-secondary)]">未绑定</span>
          ) : props.bindings.map((binding) => (
            <button
              key={binding.id}
              type="button"
              onClick={() => props.onRemove(binding.namespace_id)}
              disabled={props.busy}
              className="inline-flex items-center gap-1 rounded-full border border-[var(--border-color)] bg-[var(--bg-primary)] px-2 py-1 text-[var(--text-primary)] hover:bg-[var(--bg-secondary)] disabled:opacity-50"
              title="点击移除"
            >
              {nameByID.get(binding.namespace_id) || binding.namespace_id}
              <X className="h-3 w-3" />
            </button>
          ))}
          <div ref={pickerRef} className="relative ml-auto">
            <button
              type="button"
              onClick={() => {
                if (!props.busy && available.length > 0) {
                  setPickerOpen((open) => !open);
                }
              }}
              disabled={props.busy || available.length === 0}
              aria-haspopup="listbox"
              aria-expanded={pickerOpen && !props.busy && available.length > 0}
              className="inline-flex min-w-[160px] items-center justify-between gap-2 rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] px-2 py-1 text-left text-[var(--text-primary)] hover:bg-[var(--bg-secondary)] disabled:opacity-50"
            >
              <span className="truncate">
                {available.length === 0 ? '无可添加 KB' : selectedCount > 0 ? `已选 ${selectedCount} 个` : '选择知识库'}
              </span>
              <ChevronDown className="h-3.5 w-3.5 shrink-0" />
            </button>
            {pickerOpen && !props.busy && available.length > 0 && (
              <div
                role="listbox"
                aria-multiselectable="true"
                className="absolute bottom-full right-0 z-20 mb-2 max-h-60 w-64 overflow-auto rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] p-1 shadow-lg"
              >
                {available.map((namespace) => {
                  const checked = props.value.includes(namespace.id);
                  return (
                    <label
                      key={namespace.id}
                      role="option"
                      aria-selected={checked}
                      className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-[var(--text-primary)] hover:bg-[var(--bg-secondary)]"
                    >
                      <input
                        type="checkbox"
                        checked={checked}
                        onChange={() => props.onChange(toggleKBNamespaceSelection(props.value, namespace.id))}
                        disabled={props.busy}
                        className="sr-only"
                      />
                      <span className="flex h-4 w-4 shrink-0 items-center justify-center rounded border border-[var(--border-color)] bg-[var(--bg-card)]">
                        {checked && <Check className="h-3 w-3" />}
                      </span>
                      <span className="truncate">{namespace.name}</span>
                    </label>
                  );
                })}
              </div>
            )}
          </div>
          <button
            type="button"
            onClick={props.onAdd}
            disabled={props.busy || selectedCount === 0}
            className="inline-flex items-center gap-1 rounded-lg border border-[var(--border-color)] px-2 py-1 text-[var(--text-primary)] hover:bg-[var(--bg-secondary)] disabled:opacity-50"
          >
            <Plus className="h-3.5 w-3.5" />
            绑定
          </button>
        </div>
      </div>
    </div>
  );
}

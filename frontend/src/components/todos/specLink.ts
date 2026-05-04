export function specChangeHref(changeId?: string, revision?: number): string | null {
  const safeChangeId = (changeId || '').trim();
  if (!safeChangeId || safeChangeId === '-') return null;
  const params = new URLSearchParams({ change_id: safeChangeId });
  if (revision && revision > 0) {
    params.set('revision', String(revision));
  }
  return `/admin/spec-changes?${params.toString()}`;
}

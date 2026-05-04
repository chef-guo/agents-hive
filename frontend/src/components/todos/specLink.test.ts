import { describe, expect, it } from 'vitest';
import { specChangeHref } from './specLink';

describe('specChangeHref', () => {
  it('builds a stable admin link for projected todos', () => {
    expect(specChangeHref('harden-spec', 2)).toBe('/admin/spec-changes?change_id=harden-spec&revision=2');
  });

  it('omits revision when it is not positive', () => {
    expect(specChangeHref('harden-spec', 0)).toBe('/admin/spec-changes?change_id=harden-spec');
  });

  it('returns null without a real change id', () => {
    expect(specChangeHref('', 2)).toBeNull();
    expect(specChangeHref('-', 2)).toBeNull();
  });
});

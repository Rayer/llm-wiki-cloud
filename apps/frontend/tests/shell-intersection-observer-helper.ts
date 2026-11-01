import { vi } from 'vitest';

// jsdom has no viewport intersection API. Drive the actual Shell callback explicitly.
export function installShellIntersectionObserver() {
  const observers: {
    observe: ReturnType<typeof vi.fn>;
    disconnect: ReturnType<typeof vi.fn>;
    notify: (isIntersecting: boolean, top: number) => void;
  }[] = [];
  vi.stubGlobal('IntersectionObserver', class {
    observe = vi.fn();
    unobserve = vi.fn();
    disconnect = vi.fn();
    constructor(callback: IntersectionObserverCallback) {
      observers.push({
        observe: this.observe,
        disconnect: this.disconnect,
        notify: (isIntersecting, top) => callback([
          { isIntersecting, boundingClientRect: { top } } as IntersectionObserverEntry,
        ], this as unknown as IntersectionObserver),
      });
    }
  });
  return observers;
}

'use client';

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ComponentProps, type ReactNode } from 'react';
import Link from 'next/link';
import { useT } from '@/lib/i18n';
import { navigationDecision } from '@/lib/navigation-decision';

type NavigationBlockerValue = {
  setBlocked: (blocked: boolean) => void;
  confirmNavigation: () => boolean;
  consumeCapturedConfirmation: () => boolean;
};

const NavigationBlockerContext = createContext<NavigationBlockerValue | null>(null);

export function NavigationBlockerProvider({ children }: { children: ReactNode }) {
  // App Router has no supported API to cancel browser back/forward navigation. We guard
  // links and explicit pushes here and retain beforeunload for document exits only.
  const { t } = useT();
  const [blocked, setBlocked] = useState(false);
  const capturedConfirmation = useRef(false);
  const confirmNavigation = useCallback(
    () => !blocked || window.confirm(t('Annotation.unsavedConfirm')),
    [blocked, t],
  );

  useEffect(() => {
    if (!blocked) return;
    const beforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', beforeUnload);
    return () => window.removeEventListener('beforeunload', beforeUnload);
  }, [blocked]);

  const consumeCapturedConfirmation = useCallback(() => {
    const confirmed = capturedConfirmation.current;
    capturedConfirmation.current = false;
    return confirmed;
  }, []);
  const value = useMemo(
    () => ({ setBlocked, confirmNavigation, consumeCapturedConfirmation }),
    [confirmNavigation, consumeCapturedConfirmation],
  );
  return (
    <NavigationBlockerContext.Provider value={value}>
      <div
        onClickCapture={(event) => {
          const anchor = event.target instanceof Element ? event.target.closest<HTMLAnchorElement>('a[href]') : null;
          if (!anchor) return;
          const decision = navigationDecision({
            blocked,
            button: event.button,
            modified: event.metaKey || event.ctrlKey || event.shiftKey || event.altKey,
            target: anchor.target,
            download: anchor.hasAttribute('download'),
          });
          if (decision !== 'confirm') return;
          if (!confirmNavigation()) {
            event.preventDefault();
            return;
          }
          capturedConfirmation.current = true;
          queueMicrotask(() => { capturedConfirmation.current = false; });
        }}
      >
        {children}
      </div>
    </NavigationBlockerContext.Provider>
  );
}

export function useNavigationBlocker(): NavigationBlockerValue {
  const value = useContext(NavigationBlockerContext);
  if (!value) throw new Error('useNavigationBlocker must be used within NavigationBlockerProvider.');
  return value;
}

export function NavigationLink({ onNavigate, ...props }: ComponentProps<typeof Link>) {
  const { confirmNavigation, consumeCapturedConfirmation } = useNavigationBlocker();
  return (
    <Link
      {...props}
      onNavigate={(event) => {
        onNavigate?.(event);
        if (consumeCapturedConfirmation()) return;
        if (!confirmNavigation()) event.preventDefault();
      }}
    />
  );
}

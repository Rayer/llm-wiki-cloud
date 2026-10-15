import React from 'react';
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { configureApiAuth, getSource, getConcept } from '@/lib/api';
import { DetailClient } from '@/components/DetailClient';
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));
vi.mock('@/components/NavigationBlocker', () => ({ NavigationLink: ({children}: {children:React.ReactNode}) => <span>{children}</span>, useNavigationBlocker: () => ({setBlocked:vi.fn()}) }));
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
it.each(['source', 'concept'] as const)('retains real %s.read diagnostic ID into the existing detail ErrorState', async (kind) => {
 window.localStorage.setItem('llm-wiki-last-project', 'detail-project');
 configureApiAuth({getAccessToken: () => 'session', refreshAccessToken: async () => null, onUnauthorized: () => undefined});
 const id='detail_help_123';
 const fetchMock=vi.fn(async (url: string | URL | Request) => String(url).endsWith('/concepts') ? Response.json({concepts:[]}) : Response.json({error:'safe error',diagnostic_id:id},{status:502}));
 vi.stubGlobal('fetch',fetchMock);
 render(<DetailClient slug="fixture" label="Details" backHref="/sources" load={kind === 'source' ? getSource : getConcept} entryType={kind} />);
 expect(await screen.findByText('API request failed (502)')).toBeTruthy();
 expect(fetchMock).toHaveBeenCalledTimes(2);
 expect(screen.queryByText(id)).not.toBeNull();
});

import React from 'react';
import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { configureApiAuth } from '@/lib/api';
import { RawClient } from '@/components/RawClient';
const workspace = vi.hoisted(() => ({currentProject: {id:'race-project'}, token:'race-session'}));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => workspace }));
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));
vi.mock('next/navigation', () => ({ useSearchParams: () => new URLSearchParams() }));
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });
it('download during a pending preview must not strand the preview loading state', async () => {
  window.localStorage.setItem('llm-wiki-last-project', 'race-project');
  configureApiAuth({ getAccessToken: () => 'race-session', refreshAccessToken: async () => null, onUnauthorized: () => undefined });
  let resolvePreview!: (r: Response) => void;
  const pending = new Promise<Response>(r => { resolvePreview = r; });
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(Response.json({files:[{name:'fixture.md',size:12,updated:'',sha256:'',ingested:false}]}))
    .mockImplementationOnce(() => pending)
    .mockResolvedValueOnce(new Response('preview body'));
  vi.stubGlobal('fetch', fetchMock);
  Object.defineProperty(URL, 'createObjectURL', {configurable:true,value:vi.fn(() => 'blob:synthetic')});
  Object.defineProperty(URL, 'revokeObjectURL', {configurable:true,value:vi.fn()});
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
  render(<RawClient />);
  fireEvent.click(await screen.findByRole('button',{name:'Raw.previewAria'}));
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  expect(screen.getByText(/Raw.loadingPreview/)).toBeTruthy();
  fireEvent.click(screen.getByRole('button',{name:'Raw.download'}));
  await waitFor(() => expect(URL.createObjectURL).toHaveBeenCalledTimes(1));
  await act(async () => { resolvePreview(new Response('preview body')); await pending; });
  expect(fetchMock).toHaveBeenCalledTimes(3);
  expect(screen.queryByText(/Raw.loadingPreview/)).toBeNull();
  expect(screen.getByText('preview body')).toBeTruthy();
});

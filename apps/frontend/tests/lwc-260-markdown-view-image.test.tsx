import React from 'react';
import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';

import { MarkdownView } from '@/components/MarkdownView';

afterEach(() => {
  cleanup();
});

describe('LWC-260 MarkdownView image markdown behavior', () => {
  it('renders external markdown image URLs as native img with alt, lazy loading, and rounded class', () => {
    render(<MarkdownView content={'![Lighthouse](https://images.example.invalid/lighthouse.webp "Scenic view")'} />);

    const image = screen.getByRole('img', { name: 'Lighthouse' });
    expect(image.getAttribute('src')).toBe('https://images.example.invalid/lighthouse.webp');
    expect(image.getAttribute('alt')).toBe('Lighthouse');
    expect(image.getAttribute('loading')).toBe('lazy');
    expect(image.className).toContain('rounded-lg');
  });

  it('renders non-image markdown content without forcing image output', () => {
    render(<MarkdownView content={'Just text in a paragraph.\n\n- A list item with **bold** and `code`.'} />);

    expect(screen.getByText('Just text in a paragraph.')).toBeTruthy();
    const listItem = screen.getByRole('listitem');
    expect(listItem).toBeTruthy();
    expect(listItem.textContent).toContain('A list item with');
    expect(screen.queryByRole('img')).toBeNull();
  });

  it('ends a paragraph before an immediately following GFM table', () => {
    render(<MarkdownView content={'A paragraph immediately before a table.\n| Name | State |\n| --- | --- |\n| Source | Saved |'} />);

    expect(screen.getByText('A paragraph immediately before a table.', { selector: 'p' })).toBeTruthy();
    expect(screen.getByRole('table')).toBeTruthy();
    expect(screen.getByRole('columnheader', { name: 'Name' })).toBeTruthy();
    expect(screen.getByRole('cell', { name: 'Saved' })).toBeTruthy();
  });

  it.each([
    ['ATX', '# Source heading\n\nBody text.'],
    ['Setext', 'Source heading\n==============\n\nBody text.'],
  ])('preserves a leading %s heading for raw previews', (_style, content) => {
    render(<MarkdownView content={content} preserveLeadingHeading />);

    expect(screen.getByRole('heading', { level: 1, name: 'Source heading' })).toBeTruthy();
    expect(screen.getByText('Body text.')).toBeTruthy();
  });

  it.each([
    ['ATX', '# Source heading\n\nBody text.'],
    ['Setext', 'Source heading\n==============\n\nBody text.'],
  ])('keeps Detail page leading %s title de-duplicated', (_style, content) => {
    render(<MarkdownView content={content} />);

    expect(screen.queryByRole('heading', { level: 1, name: 'Source heading' })).toBeNull();
    expect(screen.getByText('Body text.')).toBeTruthy();
  });
});

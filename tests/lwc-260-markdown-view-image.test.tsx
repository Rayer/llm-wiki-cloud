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
});

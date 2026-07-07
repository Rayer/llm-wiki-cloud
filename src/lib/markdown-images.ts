export type MarkdownImage = {
  alt: string;
  src: string;
};

export function parseMarkdownImage(token: string): MarkdownImage | null {
  const image = /^!\[([^\]]*)\]\((.*)\)$/.exec(token.trim());
  if (!image) return null;

  const destination = imageDestination(image[2].trim());
  if (!destination) return null;

  return {
    alt: image[1] || 'Image',
    src: destination,
  };
}

function imageDestination(value: string): string {
  if (!value) return '';

  if (value.startsWith('<')) {
    const end = value.indexOf('>');
    return end > 1 ? value.slice(1, end) : '';
  }

  const titleSeparator = /\s+["'({]/.exec(value);
  return titleSeparator ? value.slice(0, titleSeparator.index) : value;
}

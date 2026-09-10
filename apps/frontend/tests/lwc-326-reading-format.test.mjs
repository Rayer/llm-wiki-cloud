import assert from 'node:assert/strict';
import test from 'node:test';
import { stripLeadingHeading } from '../src/lib/markdown-inline.ts';
import { formatPipelineDuration } from '../src/lib/pipeline-timeline.ts';

test('leading title removal tolerates frontmatter whitespace and CRLF without losing the body', () => {
  for (const text of ['\n# Title\n\nBody', '\r\n# Title\r\n\r\nBody', '\nTitle\n===\nBody']) {
    assert.equal(stripLeadingHeading(text), 'Body');
  }
  assert.equal(stripLeadingHeading('## Section\nBody'), '## Section\nBody');
  assert.equal(stripLeadingHeading('# Title'), '');
});

test('pipeline durations have readable precision and preserve unknown formats', () => {
  assert.equal(formatPipelineDuration('25.987922s', 'en'), '26 sec');
  assert.equal(formatPipelineDuration(0.125, 'en'), '0.13 sec');
  assert.equal(formatPipelineDuration('1m20s', 'en'), '1m20s');
});

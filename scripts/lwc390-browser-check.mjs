import { spawn } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const fixtureUrl = process.env.LWC390_FIXTURE_URL ?? 'http://127.0.0.1:4173/lwc390-local-fixture';
const chromePath = process.env.LWC390_CHROME_PATH ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const evidenceDirectory = new URL('../docs/lwc-390/evidence/', import.meta.url);
const evidencePath = decodeURIComponent(evidenceDirectory.pathname);
const screenshots = {
  desktop: join(evidencePath, 'lwc390-profile-desktop.png'),
  mobile: join(evidencePath, 'lwc390-profile-mobile-390.png'),
  guide: join(evidencePath, 'lwc390-guide-mobile-390.png'),
};

if (!existsSync(chromePath)) {
  throw new Error(`Chrome not found at ${chromePath}; set LWC390_CHROME_PATH to an installed Chrome binary.`);
}

const profileDirectory = mkdtempSync(join(tmpdir(), 'lwc390-chrome-'));
const chrome = spawn(chromePath, [
  '--headless=new',
  '--no-sandbox',
  '--disable-gpu',
  '--disable-dev-shm-usage',
  '--disable-background-networking',
  '--disable-extensions',
  '--disable-sync',
  '--remote-debugging-port=0',
  `--user-data-dir=${profileDirectory}`,
  'about:blank',
], { stdio: 'ignore' });

let socket;
let nextId = 1;
const pending = new Map();

function send(method, params = {}) {
  const id = nextId++;
  return new Promise((resolve, reject) => {
    pending.set(id, { resolve, reject });
    socket.send(JSON.stringify({ id, method, params }));
  });
}

function openSocket(url) {
  return new Promise((resolve, reject) => {
    socket = new WebSocket(url);
    socket.addEventListener('open', resolve, { once: true });
    socket.addEventListener('error', reject, { once: true });
    socket.addEventListener('message', (event) => {
      const message = JSON.parse(event.data);
      if (!message.id) return;
      const operation = pending.get(message.id);
      if (!operation) return;
      pending.delete(message.id);
      if (message.error) operation.reject(new Error(`${message.error.message} (${message.error.code})`));
      else operation.resolve(message.result ?? {});
    });
  });
}

async function evaluate(expression) {
  const response = await send('Runtime.evaluate', {
    expression,
    returnByValue: true,
    awaitPromise: true,
    userGesture: true,
  });
  if (response.exceptionDetails) {
    throw new Error(response.exceptionDetails.text ?? 'Browser evaluation failed');
  }
  return response.result?.value;
}

async function waitFor(expression, predicate = Boolean, label = expression, timeoutMs = 15000) {
  const until = Date.now() + timeoutMs;
  let value;
  while (Date.now() < until) {
    value = await evaluate(expression);
    if (predicate(value)) return value;
    await delay(100);
  }
  throw new Error(`Timed out waiting for ${label}; last value: ${JSON.stringify(value)}`);
}

async function clickButton(label) {
  const expression = `(() => {
    const button = [...document.querySelectorAll('button')].find((item) => {
      const text = item.getAttribute('aria-label') ?? [...item.childNodes]
        .filter((child) => !(child instanceof Element && child.getAttribute('aria-hidden') === 'true'))
        .map((child) => child.textContent ?? '')
        .join(' ')
        .replace(/\\s+/g, ' ')
        .trim();
      return text === ${JSON.stringify(label)};
    });
    if (!button) return false;
    button.click();
    return true;
  })()`;
  const clicked = await evaluate(expression);
  if (!clicked) throw new Error(`Button not found: ${label}`);
}

async function capture(path) {
  const result = await send('Page.captureScreenshot', {
    format: 'png',
    captureBeyondViewport: false,
    fromSurface: true,
  });
  writeFileSync(path, Buffer.from(result.data, 'base64'));
}

async function setViewport(width, height, mobile = false) {
  await send('Emulation.setDeviceMetricsOverride', {
    width,
    height,
    deviceScaleFactor: 1,
    mobile,
  });
  await waitFor('window.innerWidth', (value) => value === width, `viewport width ${width}`);
  await delay(150);
}

async function readGeometry() {
  return evaluate(`(() => ({
    viewport: window.innerWidth,
    document: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
    height: window.innerHeight,
    noticeVisible: (() => {
      const notice = document.querySelector('#profile-update-notice-heading')?.parentElement;
      if (!notice) return false;
      const rect = notice.getBoundingClientRect();
      return rect.width > 0 && rect.bottom <= window.innerHeight;
    })(),
    cards: ['requirements', 'interpretation', 'active'].map((key) => {
      const heading = document.querySelector('#profile-' + key + '-card-heading');
      return heading?.closest('button')?.getAttribute('aria-expanded') ?? 'missing';
    }),
  }))()`);
}

const checks = [];
function check(name, condition, details = '') {
  checks.push({ name, passed: Boolean(condition), details });
  if (!condition) throw new Error(`FAIL ${name}${details ? `: ${details}` : ''}`);
}

try {
  const activePortPath = join(profileDirectory, 'DevToolsActivePort');
  let activePort;
  for (let attempt = 0; attempt < 100 && !activePort; attempt += 1) {
    if (chrome.exitCode !== null) throw new Error(`Chrome exited with status ${chrome.exitCode}`);
    if (existsSync(activePortPath)) activePort = readFileSync(activePortPath, 'utf8').split('\n')[0];
    else await delay(100);
  }
  if (!activePort) throw new Error('Chrome did not expose a DevTools port');
  const targets = await fetch(`http://127.0.0.1:${activePort}/json/list`).then((response) => response.json());
  const page = targets.find((target) => target.type === 'page');
  if (!page?.webSocketDebuggerUrl) throw new Error('Chrome did not expose a page target');
  await openSocket(page.webSocketDebuggerUrl);
  await send('Page.enable');
  await send('Runtime.enable');
  await send('Network.enable');
  await send('Network.setCacheDisabled', { cacheDisabled: true });

  await setViewport(1280, 900, false);
  await send('Page.navigate', { url: fixtureUrl });
  await waitFor("document.querySelector('#project-profile-heading')?.textContent?.trim()", (value) => value === '整理與搜尋偏好', 'Traditional Chinese Profile title');
  await waitFor("document.querySelector('[data-testid=viewport-geometry]')?.textContent", (value) => value?.includes('overflow=false'), 'desktop fixture geometry');

  let geometry = await readGeometry();
  check('desktop has no horizontal overflow', geometry.document <= geometry.viewport, `${geometry.document}px document / ${geometry.viewport}px viewport`);
  check('desktop permanent notice is in the initial viewport', geometry.noticeVisible);
  check('desktop starts with all three cards collapsed', geometry.cards.every((state) => state === 'false'), JSON.stringify(geometry.cards));
  check('Traditional Chinese page title is localized', await evaluate("document.querySelector('#project-profile-heading')?.textContent?.trim()") === '整理與搜尋偏好');
  await clickButton('目前使用的設定');
  await waitFor("document.querySelector('#profile-active-card')?.hidden", (value) => value === false, 'active Profile card open');
  check('the previous active generation remains visible while candidate work is pending', await evaluate("document.querySelector('#profile-active-card')?.textContent?.includes('fixture-candidate-1') && document.body.innerText.includes('標記工作：執行中')"));
  await clickButton('目前使用的設定');
  await capture(screenshots.desktop);

  await clickButton('English');
  await waitFor("document.querySelector('#project-profile-heading')?.textContent?.trim()", (value) => value === 'Organization and search preferences', 'English Profile title');
  check('English notice and card labels render', await evaluate("Boolean(document.querySelector('#profile-update-notice-heading')?.textContent?.includes('What changes') && document.querySelector('#profile-requirements-card-heading')?.textContent?.trim() === 'My organization requirements')"));

  await clickButton('My organization requirements');
  await waitFor("document.querySelector('#profile-requirements-card-heading')?.closest('button')?.getAttribute('aria-expanded')", (value) => value === 'true', 'requirements card open');
  await clickButton('Add family example');
  check('example editing leaves mock API write count at zero', await evaluate("document.querySelector('[data-testid=mock-api-writes]')?.textContent?.trim()") === '0');
  check('example text is editable in the real requirements editor', await evaluate("[...document.querySelectorAll('textarea')].some((item) => item.value.includes('child seats, walkway width, and stroller access'))"));

  await clickButton('See an example');
  await waitFor("document.querySelector('#profile-guide-heading')?.textContent?.trim()", (value) => value === 'How Profile shapes organization and search', 'guide title');
  await clickButton('2. Organized knowledge');
  await waitFor("document.querySelector('#profile-guide-screen-title')?.textContent?.trim()", (value) => value === 'The same facts can be organized around different needs', 'knowledge stage');
  check('guide has one fixed case with unknown information preserved', await evaluate("document.querySelector('#profile-guide-window')?.textContent?.includes('stroller access has not been confirmed') && document.querySelector('#profile-guide-window')?.textContent?.includes('Tags · general information') && document.querySelector('#profile-guide-window')?.textContent?.includes('Tags · preference details and limits')"));
  check('guide stage jump moves keyboard focus to the screen heading', await evaluate("document.activeElement?.id") === 'profile-guide-screen-title');
  check('guide does not show generated result links or selection controls', await evaluate("!document.querySelector('#profile-guide-window')?.querySelector('a[href], input[type=checkbox], input[type=radio]')"));
  const desktopGrid = await evaluate("(() => { const grid = document.querySelector('#profile-concept-comparison-heading')?.parentElement?.nextElementSibling?.nextElementSibling; return { viewport: window.innerWidth, className: grid?.className, columns: getComputedStyle(grid).gridTemplateColumns }; })()");
  check('desktop guide comparison uses two columns', desktopGrid.columns.trim().split(/\s+/).length === 2, JSON.stringify(desktopGrid));
  await clickButton('3. Search');
  await waitFor("document.querySelector('#profile-guide-screen-title')?.textContent?.trim()", (value) => value === 'Ask one question about the same place', 'query stage');
  check('query stage preserves the stated Taipei Da’an scope', await evaluate("document.querySelector('#profile-guide-window')?.textContent?.includes('Find a restaurant in Taipei City’s Da’an District with parking information and child seats.')"));
  check('teaching flow still has no API writes before save', await evaluate("document.querySelector('[data-testid=mock-api-writes]')?.textContent?.trim()") === '0');
  await clickButton('Set my preferences');
  await waitFor("document.querySelector('#profile-requirements-card-heading')?.closest('button')?.getAttribute('aria-expanded')", (value) => value === 'true', 'return to open requirements editor');
  check('guide return preserves the unsaved example draft', await evaluate("[...document.querySelectorAll('textarea')].some((item) => item.value.includes('child seats, walkway width, and stroller access'))"));
  await clickButton('See an example');
  await setViewport(390, 844, true);
  await clickButton('2. Organized knowledge');
  await waitFor("document.querySelector('#profile-guide-screen-title')?.textContent?.trim()", (value) => value === 'The same facts can be organized around different needs', 'mobile knowledge stage');
  await capture(screenshots.guide);
  const mobileGrid = await evaluate("(() => { const grid = document.querySelector('#profile-concept-comparison-heading')?.parentElement?.nextElementSibling?.nextElementSibling; return { viewport: window.innerWidth, className: grid?.className, columns: getComputedStyle(grid).gridTemplateColumns }; })()");
  check('guide comparison uses a single column at mobile width', mobileGrid.columns.trim().split(/\s+/).length === 1, JSON.stringify(mobileGrid));
  check('390px guide has no horizontal overflow', (await readGeometry()).document <= 390);
  await clickButton('3. Search');
  await waitFor("document.querySelector('#profile-guide-screen-title')?.textContent?.trim()", (value) => value === 'Ask one question about the same place', 'mobile query stage');
  await clickButton('Set my preferences');
  await waitFor("document.querySelector('#profile-requirements-card-heading')?.closest('button')?.getAttribute('aria-expanded')", (value) => value === 'true', 'return to requirements from mobile guide');
  check('second guide return keeps the example draft unchanged', await evaluate("[...document.querySelectorAll('textarea')].some((item) => item.value.includes('child seats, walkway width, and stroller access'))"));
  await clickButton('Active · no pending work');
  await waitFor("document.querySelector('[data-testid=fixture-scenario]')?.textContent?.trim()", (value) => value === 'active-idle', 'switch to idle active fixture for explicit save');
  await waitFor("document.querySelector('#profile-requirements-card-heading')?.closest('button')?.getAttribute('aria-expanded')", (value) => value === 'false', 'idle settings card collapsed');
  await clickButton('My organization requirements');
  await waitFor("document.querySelector('#profile-requirements-card-heading')?.closest('button')?.getAttribute('aria-expanded')", (value) => value === 'true', 'idle requirements card open');
  await clickButton('Add family example');
  check('idle fixture example editing still has no API writes', await evaluate("document.querySelector('[data-testid=mock-api-writes]')?.textContent?.trim()") === '0');
  await clickButton('Save requirements');
  await waitFor("document.querySelector('[data-testid=mock-api-writes]')?.textContent?.trim()", (value) => value === '1', 'one explicit mocked save');
  check('only the explicit save sends a mocked API write', await evaluate("document.querySelector('[data-testid=mock-api-writes]')?.textContent?.trim()") === '1');

  await clickButton('Active + pending candidate');
  await waitFor("document.querySelector('[data-testid=fixture-scenario]')?.textContent?.trim()", (value) => value === 'active-pending', 'reset active scenario');
  await setViewport(390, 844, true);
  await waitFor("document.querySelector('#profile-requirements-card-heading')?.closest('button')?.getAttribute('aria-expanded')", (value) => value === 'false', 'collapsed mobile settings');
  await waitFor("document.querySelector('[data-testid=viewport-geometry]')?.textContent", (value) => value?.includes('viewport=390px') && value.includes('overflow=false'), '390px fixture geometry');
  geometry = await readGeometry();
  check('390px viewport has no horizontal overflow', geometry.document <= geometry.viewport, `${geometry.document}px document / ${geometry.viewport}px viewport`);
  check('390px initial viewport keeps the permanent notice readable', geometry.noticeVisible);
  check('390px initial viewport leaves all settings cards collapsed', geometry.cards.every((state) => state === 'false'), JSON.stringify(geometry.cards));
  await capture(screenshots.mobile);

  await clickButton('繁體中文');
  await clickButton('No active · first compile');
  await waitFor("document.querySelector('[data-testid=fixture-scenario]')?.textContent?.trim()", (value) => value === 'first-compile', 'first-compile scenario');
  await clickButton('系統如何理解我的需求');
  await waitFor("document.querySelector('#profile-bootstrap-heading')?.textContent?.trim()", (value) => value === '首次編譯指引', 'first-compile guidance');
  check('first-compile guidance is a preview and remains unconfirmed before user action', await evaluate("document.querySelector('#profile-interpretation-card')?.textContent?.includes('預覽已就緒') && document.querySelector('[data-testid=mock-api-writes]')?.textContent?.trim() === '1'"));
  await clickButton('English');
  await waitFor("document.querySelector('#profile-bootstrap-heading')?.textContent?.trim()", (value) => value === 'First-compile guidance', 'English first-compile guidance');
  await clickButton('Confirm for first compile');
  await waitFor("document.querySelector('#profile-interpretation-card')?.textContent?.includes('Confirmed for first compile')", Boolean, 'confirmed first-compile guidance');
  check('confirmation does not activate a generation-bound Profile', await evaluate("document.querySelector('#profile-active-card')?.textContent?.includes('No generation-bound Profile is active yet') && document.querySelector('[data-testid=mock-api-writes]')?.textContent?.trim() === '2'"));

  const finalGeometry = await readGeometry();
  check('first-compile case remains within 390px viewport', finalGeometry.document <= finalGeometry.viewport, `${finalGeometry.document}px document / ${finalGeometry.viewport}px viewport`);
  await capture(join(evidencePath, 'lwc390-first-compile-mobile-390.png'));

  for (const result of checks) {
    process.stdout.write(`PASS ${result.name}${result.details ? ` — ${result.details}` : ''}\n`);
  }
  process.stdout.write(`SCREENSHOT ${screenshots.desktop}\nSCREENSHOT ${screenshots.mobile}\nSCREENSHOT ${screenshots.guide}\n`);
} finally {
  socket?.close();
  chrome.kill('SIGTERM');
  await delay(250);
  rmSync(profileDirectory, { recursive: true, force: true });
}

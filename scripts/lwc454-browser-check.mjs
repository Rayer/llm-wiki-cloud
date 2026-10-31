import { spawn } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const repo = resolve(new URL('..', import.meta.url).pathname);
const ui = process.env.LWC454_UI_URL ?? 'http://127.0.0.1:4173';
const fixture = process.env.LWC454_FIXTURE_URL ?? 'http://127.0.0.1:4174';
const chromePath = process.env.LWC454_CHROME_PATH ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const evidence = join(repo, '.build/local-uat/evidence');
const resultsPath = join(repo, '.build/local-uat/cases.json');
const fixtureFile = join(repo, '.build/local-uat/fixtures/meeting-notes.docx');
mkdirSync(evidence, { recursive: true });
mkdirSync(join(repo, '.build/local-uat/fixtures'), { recursive: true });
writeFileSync(fixtureFile, 'SYNTHETIC LOCAL DOCUMENT FIXTURE. No provider calls or live data.\n');
if (!existsSync(chromePath)) throw new Error(`Chrome binary not found: ${chromePath}`);

const chromeProfile = mkdtempSync(join(process.env.TMPDIR ?? '/tmp', 'lwc454-chrome-'));
const chrome = spawn(chromePath, [
  '--headless=new', '--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage',
  '--disable-background-networking', '--disable-extensions', '--disable-sync',
  '--remote-debugging-port=0', `--user-data-dir=${chromeProfile}`, 'about:blank',
], { stdio: 'ignore' });

let socket;
let nextId = 1;
const pending = new Map();
const checks = [];

function check(id, passed, details = '') {
  checks.push({ id, passed: Boolean(passed), details });
  if (!passed) throw new Error(`${id}: ${details || 'assertion failed'}`);
}

function openSocket(url) {
  return new Promise((resolveOpen, reject) => {
    socket = new WebSocket(url);
    socket.addEventListener('open', resolveOpen, { once: true });
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

function send(method, params = {}) {
  const id = nextId++;
  return new Promise((resolveSend, reject) => {
    const timeout = setTimeout(() => {
      pending.delete(id);
      reject(new Error(`Chrome DevTools command timed out: ${method}`));
    }, 8000);
    pending.set(id, {
      resolve: (value) => { clearTimeout(timeout); resolveSend(value); },
      reject: (error) => { clearTimeout(timeout); reject(error); },
    });
    socket.send(JSON.stringify({ id, method, params }));
  });
}

async function evaluate(expression) {
  const response = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true, userGesture: true });
  if (response.exceptionDetails) throw new Error(response.exceptionDetails.text ?? 'Browser evaluation failed');
  return response.result?.value;
}

async function waitFor(expression, predicate = Boolean, label = expression, timeoutMs = 20000) {
  const until = Date.now() + timeoutMs;
  let value;
  while (Date.now() < until) {
    value = await evaluate(expression);
    if (predicate(value)) return value;
    await delay(120);
  }
  throw new Error(`Timed out waiting for ${label}; last value: ${JSON.stringify(value)}`);
}

async function navigate(path) {
  const changed = await evaluate(`(() => {
    if (location.origin === 'null') return false;
    const target = new URL(${JSON.stringify(path)}, location.origin);
    if (location.pathname === target.pathname && location.search === target.search) return true;
    const link = [...document.querySelectorAll('a[href]')].find((item) => new URL(item.href).pathname === target.pathname);
    if (link) { link.click(); return true; }
    if (location.origin === ${JSON.stringify(new URL(ui).origin)}) {
      window.history.pushState({}, '', target.href);
      return true;
    }
    return false;
  })()`);
  if (!changed) await send('Page.navigate', { url: `${ui}${path}` });
  const includesQuery = new URL(path, ui).search !== '';
  await waitFor(includesQuery ? 'location.pathname + location.search' : 'location.pathname', (value) => value === (includesQuery ? path : new URL(path, ui).pathname), `route ${path}`);
  await waitFor('document.readyState', (value) => value === 'complete', `load ${path}`);
}

async function clickButton(label) {
  const clicked = await evaluate(`(() => { const list=[...document.querySelectorAll('button')]; const button=list.find((item)=>((item.getAttribute('aria-label')??item.innerText??'').replace(/\\s+/g,' ').trim())===${JSON.stringify(label)}); if(!button)return false; button.focus(); button.click(); return true; })()`);
  if (!clicked) throw new Error(`Button not found: ${label}`);
}

async function clickButtonContaining(text) {
  const clicked = await evaluate(`(() => { const button=[...document.querySelectorAll('button')].find((item)=>((item.innerText??'').replace(/\\s+/g,' ').trim()).includes(${JSON.stringify(text)})); if(!button)return false; button.focus(); button.click(); return true; })()`);
  if (!clicked) throw new Error(`Button containing text not found: ${text}`);
}

async function clickLinkContaining(text) {
  const clicked = await evaluate(`(() => { const link=[...document.querySelectorAll('a[href]')].find((item)=>((item.innerText??'').replace(/\\s+/g,' ').trim()).includes(${JSON.stringify(text)})); if(!link)return false; link.focus(); link.click(); return true; })()`);
  if (!clicked) throw new Error(`Link containing text not found: ${text}`);
}

async function changeSelect(selector, value) {
  const changed = await evaluate(`(() => { const item=document.querySelector(${JSON.stringify(selector)}); if(!item)return false; const setter=Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype,'value').set; setter.call(item,${JSON.stringify(value)}); item.dispatchEvent(new Event('change',{bubbles:true})); return true; })()`);
  if (!changed) throw new Error(`Select not found: ${selector}`);
}

async function key(key, shiftKey = false) {
  await send('Input.dispatchKeyEvent', { type: 'keyDown', key, code: key === 'Escape' ? 'Escape' : 'Tab', modifiers: shiftKey ? 8 : 0 });
  await send('Input.dispatchKeyEvent', { type: 'keyUp', key, code: key === 'Escape' ? 'Escape' : 'Tab', modifiers: shiftKey ? 8 : 0 });
}

async function capture(name) {
  const image = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false, fromSurface: true });
  writeFileSync(join(evidence, name), Buffer.from(image.data, 'base64'));
}

async function viewport(width, height, mobile = false) {
  await send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile });
  await waitFor('window.innerWidth', (value) => value === width, `viewport ${width}x${height}`);
  await delay(250);
}

async function setFixture(values) {
  const response = await fetch(`${fixture}/__fixture/state`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(values) });
  if (!response.ok) throw new Error(`Fixture control failed (${response.status})`);
  return response.json();
}

async function waitForFixture(predicate, label, timeoutMs = 10000) {
  const until = Date.now() + timeoutMs;
  let value;
  while (Date.now() < until) {
    value = await fetch(`${fixture}/__fixture/state`).then((response) => response.json());
    if (predicate(value)) return value;
    await delay(120);
  }
  throw new Error(`Timed out waiting for local fixture ${label}`);
}

async function dialogGeometry() {
  return evaluate(`(() => { const d=document.querySelector('[role="dialog"][aria-labelledby="account-settings-title"]'); if(!d)return null; const r=d.getBoundingClientRect(); const nodes=[...d.querySelectorAll('a[href],button:not(:disabled),input:not(:disabled),select:not(:disabled),textarea:not(:disabled),[tabindex]:not([tabindex="-1"])')].filter(x=>!x.closest('[hidden]')); const max=Math.max(0,d.scrollHeight-d.clientHeight); d.scrollTop=max; const last=nodes.at(-1); const lr=last?.getBoundingClientRect(); return {x:r.x,y:r.y,width:r.width,height:r.height,scrollHeight:d.scrollHeight,clientHeight:d.clientHeight,scrollTop:d.scrollTop,maxScroll:max,lastTag:last?.tagName,lastText:last?.innerText,lastBottom:lr?.bottom,lastTop:lr?.top,focusables:nodes.length}; })()`);
}

const fixtureReset = await fetch(`${fixture}/__fixture/state`, {
  method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ reset: true, bootstrap: 'unavailable' }),
});
if (!fixtureReset.ok) throw new Error(`Could not reset local fixture (${fixtureReset.status})`);

try {
  const portFile = join(chromeProfile, 'DevToolsActivePort');
  let port;
  for (let attempt = 0; attempt < 100 && !port; attempt += 1) {
    if (chrome.exitCode !== null) throw new Error(`Chrome exited with ${chrome.exitCode}`);
    if (existsSync(portFile)) port = readFileSync(portFile, 'utf8').split('\n')[0];
    else await delay(100);
  }
  if (!port) throw new Error('Chrome did not expose DevTools');
  const targets = await fetch(`http://127.0.0.1:${port}/json/list`).then((response) => response.json());
  const page = targets.find((item) => item.type === 'page');
  if (!page?.webSocketDebuggerUrl) throw new Error('Chrome page target missing');
  await openSocket(page.webSocketDebuggerUrl);
  await send('Page.enable'); await send('Runtime.enable'); await send('DOM.enable'); await send('Network.enable');
  await send('Network.setCacheDisabled', { cacheDisabled: true });
  await send('Page.addScriptToEvaluateOnNewDocument', { source: `(() => { const install=()=>{ if(!document.body||document.getElementById('lwc454-local-fixture-banner'))return;const b=document.createElement('div');b.id='lwc454-local-fixture-banner';b.textContent='LOCAL SYNTHETIC FIXTURE ONLY · no cloud/provider calls';b.style='position:fixed;right:8px;top:4px;z-index:2147483647;padding:4px 8px;background:#5b214d;color:white;font:12px/1.2 sans-serif;border:1px solid #f0a6da;border-radius:6px;pointer-events:none';document.body.append(b);};new MutationObserver(install).observe(document.documentElement,{childList:true,subtree:true});install();})();` });

  await viewport(1280, 900);
  await navigate('/lwc454-local-entry.html');
  const localEntry = await evaluate("({label:document.querySelector('#lwc454-local-fixture-banner')?.innerText,buttons:[...document.querySelectorAll('button')].map((button)=>({label:button.getAttribute('aria-label'),text:button.innerText.trim(),visible:button.getBoundingClientRect().width>0&&button.getBoundingClientRect().height>0}))})");
  check('lwc454-local-entry-button-visible', localEntry?.label?.includes('LOCAL SYNTHETIC LAB ONLY') && localEntry?.buttons?.length === 1 && localEntry.buttons[0].visible, JSON.stringify(localEntry));
  await capture('local-entry-lab-1280x900.png');
  await clickButton('Enter local synthetic preview');
  await waitFor("location.pathname === '/'", Boolean, 'entry button opens the actual product homepage');
  await waitFor("Boolean(document.querySelector('[data-testid=" + JSON.stringify('pipeline-automation-settings') + "]'))", Boolean, 'actual product homepage from the local entry');
  const entryIdentity = await evaluate("({token:localStorage.getItem('llm-wiki-access-token'),user:JSON.parse(localStorage.getItem('llm-wiki-auth-user')||'null'),locale:localStorage.getItem('locale'),project:localStorage.getItem('llm-wiki-last-project')})");
  check('lwc454-local-entry-declared-identity-and-zh-tw', entryIdentity?.token === 'fixture-token-only' && entryIdentity?.user?.id === 'fixture-user-1' && entryIdentity?.user?.email === 'fixture@example.invalid' && entryIdentity?.locale === 'zh-TW' && entryIdentity?.project === 'fixture-project-1', JSON.stringify(entryIdentity));
  check('lwc454-local-entry-opens-product-homepage', await evaluate("location.pathname === '/' && Boolean(document.querySelector('[data-testid=" + JSON.stringify('pipeline-automation-settings') + "]'))"), 'The visible entry button opened the existing product homepage after loading only the declared fixture identity.');
  const languageLabel = await evaluate("[...document.querySelectorAll('select')].find((item) => [...item.options].some((option) => option.value === 'en'))?.getAttribute('aria-label')");
  if (!languageLabel) throw new Error('Product locale selector is unavailable after entering the synthetic preview.');
  await changeSelect(`select[aria-label=${JSON.stringify(languageLabel)}]`, 'en');
  await waitFor("[...document.querySelectorAll('select')].some((item) => item.value === 'en')", Boolean, 'English for the remaining existing browser checks');
  await setFixture({ bootstrap: 'unavailable' });
  await evaluate("localStorage.removeItem('llm-wiki-last-project')");
  await send('Page.reload');
  await waitFor("document.body.innerText.includes('Workspace setup pending')", Boolean, 'local unavailable bootstrap fixture');
  check('458-unavailable-fixture', await evaluate("document.body.innerText.includes('account access unavailable')"), 'WorkspaceProvider displays the synthetic 503 response.');
  await setFixture({ bootstrap: 'unauthorized' });
  await clickButton('Try again');
  await waitFor("document.body.innerText.includes('invalid token')", Boolean, 'local unauthorized bootstrap fixture');
  check('458-unauthorized-fixture', await evaluate("document.body.innerText.includes('invalid token')"), 'Synthetic 401 remains distinct from the 503 response.');
  await setFixture({ bootstrap: 'ready' });
  await clickButton('Try again');
  await waitFor("localStorage.getItem('llm-wiki-last-project')", (value) => value === 'fixture-project-1', 'retry success and actual Project selection');
  process.stdout.write('checkpoint: WorkspaceProvider fixtures complete\n');
  const bootstrapState = await fetch(`${fixture}/__fixture/state`).then((response) => response.json());
  check('458-retry-success-fixture', await evaluate("localStorage.getItem('llm-wiki-last-project') === 'fixture-project-1'")
    && bootstrapState.requests.some((request) => request.method === 'POST' && request.path === '/api/v1/onboarding/default-project' && request.status === 200)
    && bootstrapState.requests.some((request) => request.method === 'GET' && request.path === '/api/v1/projects' && request.status === 200), 'The real WorkspaceProvider accepted a local ready response and selected the fixture Project after retry.');
  const permission = await fetch(`${fixture}/api/v1/projects/fixture-project-1/private-check`, { headers: { Authorization: 'Bearer fixture-token-only', 'X-Project-ID': 'fixture-project-1' } });
  check('458-negative-permission-fixture', permission.status === 403, 'Local denied project access remains HTTP 403.');

  await capture('home-desktop-normal-1280x900.png');
  check('454-home-settings-guide-no-toggle', await evaluate("Boolean(document.querySelector('[data-testid=" + JSON.stringify('pipeline-automation-settings') + "]')) && !document.querySelector('[data-testid=" + JSON.stringify('pipeline-automation-project-toggle') + "]') && document.querySelector('a[href=" + JSON.stringify('/profile#project-pipeline-automation') + "]')?.innerText.includes('Open Pipeline settings')"), 'Pipeline home shows status and a link to its Project settings tab, without the removed switch.');
  check('454-home-description-link-target', await evaluate("(() => {const link=document.querySelector('a[href=" + JSON.stringify('/profile#project-description') + "]');return Boolean(link && ['Add description','Edit description'].includes(link.innerText.trim()))})()"), 'Home description editor link targets its Project information tab and saved description field.');
  const homeDescriptionLabel = await evaluate("document.querySelector('a[href=" + JSON.stringify('/profile#project-description') + "]')?.innerText.trim()");
  await clickLinkContaining(homeDescriptionLabel);
  await waitFor("document.querySelector('#project-profile-heading')?.textContent?.trim()", (value) => value === 'Project settings', 'Profile route');
  await waitFor("location.hash === '#project-description' && document.querySelector('#project-settings-tab-project-info')?.getAttribute('aria-selected') === 'true'", Boolean, 'Project information hash tab from Home');
  await waitFor("document.querySelector('#project-description-input')?.value", (value) => value === 'Local fixture project description', 'description draft loaded');
  await waitFor("document.activeElement?.id === 'project-description-input'", Boolean, 'Home link focus in actual description field');
  check('454-home-description-hash-route', await evaluate("Boolean(document.querySelector('#project-settings-panel-project-info #project-description-input')) && !document.querySelector('#project-settings-panel-project-info').hidden"), 'Home opens the visible Project information tab and focuses the editable description.');
  await send('Page.navigate', { url: `${ui}/` });
  await waitFor('location.pathname', (value) => value === '/', 'fresh Homepage route');
  await waitFor("Boolean(document.querySelector('#home-project-description-heading'))", Boolean, 'reloaded Home content');
  await waitFor("Boolean(document.querySelector('a[href=" + JSON.stringify('/profile#project-pipeline-automation') + "]'))", Boolean, 'Home Pipeline settings guide link');
  await clickLinkContaining('Open Pipeline settings');
  await waitFor("location.pathname + location.hash", (value) => value === '/profile#project-pipeline-automation', 'Pipeline guide route from Home');
  await waitFor("document.querySelector('#project-settings-tab-pipeline')?.getAttribute('aria-selected') === 'true'", Boolean, 'Pipeline guide hash tab from Home');
  await waitFor("document.activeElement?.id === 'project-pipeline-automation'", Boolean, 'Pipeline guide focus in automation settings');
  await waitFor("Boolean(document.querySelector('[data-testid=" + JSON.stringify('project-automation-settings') + "] [data-testid=" + JSON.stringify('pipeline-automation-project-toggle') + "]'))", Boolean, 'Project automation switch in settings tab');
  await waitFor("document.querySelector('#project-settings-panel-pipeline')?.hidden === false", Boolean, 'Project Pipeline settings tab');
  check('454-settings-sibling-panels', await evaluate("Boolean(document.querySelector('#project-settings-panel-project-info #project-description-input')) && Boolean(document.querySelector('#project-settings-panel-project-info').innerText.includes('Saved summary')) && Boolean(document.querySelector('#project-settings-panel-pipeline [data-testid=" + JSON.stringify('project-automation-settings') + "] [data-testid=" + JSON.stringify('pipeline-automation-project-toggle') + "]')) && !document.querySelector('#project-settings-panel-knowledge #project-description-input') && !document.querySelector('#project-settings-panel-knowledge [data-testid=" + JSON.stringify('pipeline-automation-project-toggle') + "]')"), 'Project information, Pipeline automation, knowledge generation, and LLM Provider are sibling panels.');
  check('454-global-disable-copy', await evaluate("document.querySelector('[data-testid=" + JSON.stringify('pipeline-automation-blocked') + "]')?.innerText.includes('regular users cannot enable the global setting')"), 'Global disabled copy reserves the global switch for administrators.');
  await clickButtonContaining('Knowledge generation settings');
  await waitFor("document.querySelector('#project-settings-panel-knowledge')?.hidden === false", Boolean, 'knowledge generation sibling tab');
  check('454-knowledge-panel-scope', await evaluate("Boolean(document.querySelector('#project-settings-panel-knowledge h3')) && !document.querySelector('#project-settings-panel-knowledge #project-description-input') && !document.querySelector('#project-settings-panel-knowledge [data-testid=" + JSON.stringify('pipeline-automation-project-toggle') + "]')"), 'Knowledge generation tab contains Profile controls without Project information or Pipeline settings.');
  await clickButtonContaining('Project information');
  await waitFor("document.querySelector('#project-settings-tab-project-info')?.getAttribute('aria-selected') === 'true'", Boolean, 'Project information sibling tab');
  const settingsTabs = await evaluate("(() => {const tabs=[...document.querySelectorAll('[role=tablist] [role=tab]')];return {list:tabs.map((tab) => ({name:tab.innerText.trim(),selected:tab.getAttribute('aria-selected'),controls:document.getElementById(tab.getAttribute('aria-controls'))?.getAttribute('aria-labelledby')})),valid:tabs.length===4&&tabs.every((tab)=>document.getElementById(tab.getAttribute('aria-controls'))?.getAttribute('aria-labelledby')===tab.id)}})()");
  check('454-four-settings-tabs-aria', settingsTabs?.valid === true && settingsTabs?.list?.length === 4, JSON.stringify(settingsTabs));
  const settingsLayoutGeometry = [];
  await capture('project-settings-desktop-normal-1280x900.png');
  await viewport(1280, 577);
  const shortLayout = await evaluate("(() => {const t=document.querySelector('[role=tablist]');const r=t.getBoundingClientRect();const p=document.querySelector('#project-settings-panel-project-info');const pr=p.getBoundingClientRect();return {viewport:{width:innerWidth,height:innerHeight},document:{width:Math.max(document.documentElement.scrollWidth,document.body.scrollWidth),height:Math.max(document.documentElement.scrollHeight,document.body.scrollHeight),scrollY},tablist:{x:r.x,y:r.y,width:r.width,height:r.height,scrollWidth:t.scrollWidth,clientWidth:t.clientWidth},tabs:[...t.querySelectorAll('[role=tab]')].map((x)=>{const b=x.getBoundingClientRect();return {label:x.innerText.trim(),x:b.x,y:b.y,width:b.width,height:b.height}}),activePanel:{x:pr.x,y:pr.y,width:pr.width,height:pr.height,scrollTop:p.scrollTop,scrollHeight:p.scrollHeight,clientHeight:p.clientHeight}}})()");
  settingsLayoutGeometry.push({viewport:'1280x577',...shortLayout});
  check('454-tabs-short-desktop-fit', Boolean(shortLayout && shortLayout.tablist.x >= 0 && shortLayout.tablist.x + shortLayout.tablist.width <= 1280 && shortLayout.tabs.length === 4 && shortLayout.document.width <= 1280), JSON.stringify(shortLayout));
  await capture('project-settings-desktop-short-1280x577.png');
  await viewport(390, 844, true);
  const mobileLayout = await evaluate("(() => {const t=document.querySelector('[role=tablist]');const r=t.getBoundingClientRect();const p=document.querySelector('#project-settings-panel-project-info');const pr=p.getBoundingClientRect();return {viewport:{width:innerWidth,height:innerHeight},document:{width:Math.max(document.documentElement.scrollWidth,document.body.scrollWidth),height:Math.max(document.documentElement.scrollHeight,document.body.scrollHeight),scrollY},tablist:{x:r.x,y:r.y,width:r.width,height:r.height,scrollWidth:t.scrollWidth,clientWidth:t.clientWidth},tabs:[...t.querySelectorAll('[role=tab]')].map((x)=>{const b=x.getBoundingClientRect();return {label:x.innerText.trim(),x:b.x,y:b.y,width:b.width,height:b.height}}),activePanel:{x:pr.x,y:pr.y,width:pr.width,height:pr.height,scrollTop:p.scrollTop,scrollHeight:p.scrollHeight,clientHeight:p.clientHeight}}})()");
  settingsLayoutGeometry.push({viewport:'390x844',...mobileLayout});
  const settingsOverflow = mobileLayout?.document?.width;
  check('settings-mobile-no-horizontal-overflow', settingsOverflow <= 390, `document width=${settingsOverflow}px at 390px viewport.`);
  check('454-tabs-mobile-fit', Boolean(mobileLayout && mobileLayout.tablist.x >= 0 && mobileLayout.tablist.x + mobileLayout.tablist.width <= 390 && mobileLayout.tabs.length === 4), JSON.stringify(mobileLayout));
  await capture('project-settings-mobile-390x844.png');
  await viewport(1280, 900);
  process.stdout.write('checkpoint: settings layout complete\n');

  const setDraft = await evaluate(`(() => { const el=document.querySelector('#project-settings-panel-project-info textarea');if(!el)return false;const setter=Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype,'value').set;setter.call(el,'Unsaved local settings draft');el.dispatchEvent(new Event('input',{bubbles:true}));el.dispatchEvent(new Event('change',{bubbles:true}));return true;})()`);
  check('454-project-description-draft-entered', setDraft, 'The visible description editor accepted a local unsaved draft.');
  await clickButtonContaining('LLM Provider');
  await waitFor("document.querySelector('#project-settings-panel-ai-service')?.hidden === false", Boolean, 'Project Provider tab');
  await changeSelect('#project-settings-panel-ai-service select', 'personal');
  await clickButtonContaining('Open LLM Provider');
  process.stdout.write('checkpoint: Project-entry account modal opened\n');
  await waitFor("Boolean(document.querySelector('[role=" + JSON.stringify('dialog') + "][aria-labelledby=" + JSON.stringify('account-settings-title') + "]'))", Boolean, 'Project Provider account modal');
  check('457-project-entry-modal-portal', await evaluate("document.querySelector('[aria-labelledby=" + JSON.stringify('account-settings-title') + "]')?.parentElement?.parentElement === document.body"), 'Account modal is mounted outside Project backdrops.');
  await waitFor("Boolean(document.querySelector('#byok-profiles-heading'))", Boolean, 'Project entry provider panel');
  await waitFor("document.querySelector('#llm-source-fixture-project-1')?.closest('section')?.innerText.includes('Platform shared service')", Boolean, 'synthetic shared Project binding loaded');
  const bindingEvidence = await evaluate("(() => {const panel=document.querySelector('#llm-source-fixture-project-1')?.closest('section');return {text:panel?.innerText,alerts:panel?.querySelectorAll('[role=alert]').length};})()");
  check('457-project-provider-fixture-is-successful', bindingEvidence?.text?.includes('Platform shared service') && bindingEvidence.alerts === 0, JSON.stringify(bindingEvidence));
  const projectEntrySizes = [[1280,900,false,'normal-1280x900'],[1280,577,false,'short-1280x577'],[390,844,true,'mobile-390x844']];
  const projectModalGeometry = [];
  for (const [w,h,mobile,label] of projectEntrySizes) {
    await viewport(w,h,mobile);
    const geometry = await dialogGeometry();
    projectModalGeometry.push({viewport:`${w}x${h}`,...geometry});
    check(`457-project-entry-bottom-${label}`, Boolean(geometry && geometry.scrollTop === geometry.maxScroll && geometry.lastTop >= 0 && geometry.lastBottom <= h), JSON.stringify(geometry));
    check(`457-project-entry-bounds-${label}`, Boolean(geometry && geometry.x >= 0 && geometry.y >= 0 && geometry.x + geometry.width <= w && geometry.y + geometry.height <= h), JSON.stringify(geometry));
    await capture(`project-entry-account-modal-${label}.png`);
  }
  const focusCheck = await evaluate(`(() => { const d=document.querySelector('[role="dialog"][aria-labelledby="account-settings-title"]');const f=[...d.querySelectorAll('a[href],button:not(:disabled),input:not(:disabled),select:not(:disabled),textarea:not(:disabled),[tabindex]:not([tabindex="-1"])')].filter(x=>!x.closest('[hidden]'));if(!f.length)return null;f.at(-1).focus();return {first:f[0].getAttribute('aria-label')||f[0].innerText,last:f.at(-1).getAttribute('aria-label')||f.at(-1).innerText};})()`);
  await key('Tab');
  check('457-project-entry-focus-trap', await evaluate("document.activeElement === document.querySelector('[role=" + JSON.stringify('dialog') + "][aria-labelledby=" + JSON.stringify('account-settings-title') + "]')?.querySelector('button[aria-label=" + JSON.stringify('Close account settings') + "]')"), `Focusables before Tab: ${JSON.stringify(focusCheck)}; Tab wraps within the modal.`);
  await key('Escape');
  await waitFor("!document.querySelector('[role=" + JSON.stringify('dialog') + "][aria-labelledby=" + JSON.stringify('account-settings-title') + "]')", Boolean, 'Project-entry cancel/return');
  check('457-project-entry-return-focus-and-draft', await evaluate("document.activeElement?.innerText.includes('Open LLM Provider') && document.querySelector('#project-settings-panel-project-info textarea')?.value === 'Unsaved local settings draft'"), 'Escape returns focus to the Project Provider trigger and preserves the Project information draft.');
  const afterCancel = await fetch(`${fixture}/__fixture/state`).then((response) => response.json());
  check('457-cancel-no-project-description-write', afterCancel.requests.filter((request) => request.method === 'PUT' && request.path.endsWith('/description')).length === 0, 'The unsaved description draft was not written during modal cancellation.');
  check('457-discard-local-draft-after-cancel-check', await evaluate(`(() => { const el=document.querySelector('#project-settings-panel-project-info textarea');if(!el)return false;const setter=Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype,'value').set;setter.call(el,'Local fixture project description');el.dispatchEvent(new Event('input',{bubbles:true}));el.dispatchEvent(new Event('change',{bubbles:true}));return true;})()`), 'Reset only the browser-local draft after verifying that cancellation preserved it.');

  await viewport(1280,900);
  await clickButton('Account settings');
  await waitFor("Boolean(document.querySelector('[role=" + JSON.stringify('dialog') + "][aria-labelledby=" + JSON.stringify('account-settings-title') + "]'))", Boolean, 'direct account entry');
  check('457-direct-account-entry', await evaluate("document.querySelector('#account-settings-tab-account')?.getAttribute('aria-selected') === 'true'"), 'Direct account entry opens the account tab.');
  const directSizes = [[1280,900,false,'normal-1280x900'],[1280,577,false,'short-1280x577']];
  const directModalGeometry = [];
  for (const [w,h,mobile,label] of directSizes) {
    await viewport(w,h,mobile);
    const geometry = await dialogGeometry();
    directModalGeometry.push({viewport:`${w}x${h}`,...geometry});
    check(`457-direct-entry-bottom-${label}`, Boolean(geometry && geometry.scrollTop === geometry.maxScroll && geometry.lastTop >= 0 && geometry.lastBottom <= h), JSON.stringify(geometry));
    check(`457-direct-entry-bounds-${label}`, Boolean(geometry && geometry.x >= 0 && geometry.y >= 0 && geometry.x + geometry.width <= w && geometry.y + geometry.height <= h), JSON.stringify(geometry));
    await capture(`direct-account-modal-${label}.png`);
  }
  await key('Escape');
  await waitFor("!document.querySelector('[role=" + JSON.stringify('dialog') + "][aria-labelledby=" + JSON.stringify('account-settings-title') + "]')", Boolean, 'direct modal close');
  check('457-direct-entry-return-focus', await evaluate("document.activeElement?.innerText.trim() === 'Account settings'"), 'Direct account close returns focus to its sidebar trigger.');

  await viewport(390,844,true);
  await clickButton('Open navigation');
  await clickButton('Account settings');
  await waitFor("Boolean(document.querySelector('[role=" + JSON.stringify('dialog') + "][aria-labelledby=" + JSON.stringify('account-settings-title') + "]'))", Boolean, 'mobile direct account entry');
  const mobileDirect = await dialogGeometry();
  directModalGeometry.push({viewport:'390x844',...mobileDirect});
  check('457-direct-entry-bottom-mobile-390x844', Boolean(mobileDirect && mobileDirect.scrollTop === mobileDirect.maxScroll && mobileDirect.lastTop >= 0 && mobileDirect.lastBottom <= 844), JSON.stringify(mobileDirect));
  check('457-direct-entry-bounds-mobile-390x844', Boolean(mobileDirect && mobileDirect.x >= 0 && mobileDirect.y >= 0 && mobileDirect.x + mobileDirect.width <= 390 && mobileDirect.y + mobileDirect.height <= 844), JSON.stringify(mobileDirect));
  await capture('direct-account-modal-mobile-390x844.png');
  await key('Escape');
  await waitFor("!document.querySelector('[role=" + JSON.stringify('dialog') + "][aria-labelledby=" + JSON.stringify('account-settings-title') + "]')", Boolean, 'mobile direct modal close');
  check('457-direct-entry-return-focus-mobile', await evaluate("document.activeElement?.innerText.trim() === 'Account settings'"), 'Mobile direct close returns focus into the open navigation drawer.');
  await clickButton('Close navigation');
  process.stdout.write('checkpoint: direct account modal checks complete\n');

  await viewport(1280,900);
  process.stdout.write('checkpoint: opening real homepage upload\n');
  await navigate('/');
  await waitFor("Boolean(document.querySelector('input[type=file]#raw-file-upload'))", Boolean, 'Raw upload control');
  const fileDocument = await send('DOM.getDocument', { depth: -1, pierce: true });
  const fileInput = await send('DOM.querySelector', { nodeId: fileDocument.root.nodeId, selector: 'input[type=file]#raw-file-upload' });
  if (!fileInput.nodeId) throw new Error('Raw file input missing from real product route');
  await send('DOM.setFileInputFiles', { nodeId: fileInput.nodeId, files: [fixtureFile] });
  await evaluate("document.querySelector('input[type=file]#raw-file-upload')?.dispatchEvent(new Event('change',{bubbles:true}))");
  await waitFor("document.body.innerText.includes('Accepted')", Boolean, 'real RawUploadPanel accepts local synthetic import');
  process.stdout.write('checkpoint: synthetic import accepted\n');
  check('455-import-accepted-through-product', await evaluate("document.body.innerText.includes('meeting-notes.docx')"), 'The actual product upload control accepted the synthetic document and is polling local fixture status.');
  check('455-upload-source-status-is-human-readable', await evaluate("document.body.innerText.includes('not saved')"), 'The real upload row displays the API not_started state through the existing translated not saved label.');
  await navigate('/sources');
  await waitFor("Boolean(document.querySelector('section[aria-labelledby=" + JSON.stringify('document-imports-title') + "] select'))", Boolean, 'Sources and import list');
  await changeSelect('section[aria-labelledby="document-imports-title"] select', 'all');
  await waitFor("document.body.innerText.includes('converting') || document.body.innerText.includes('Converting')", Boolean, 'document import in progress');
  await capture('document-import-in-progress-desktop-1280x900.png');
  check('455-import-in-progress', await evaluate("document.body.innerText.includes('converting') || document.body.innerText.includes('Converting')"), 'Product import history displays the in-progress state.');
  await setFixture({ finishImport: true });
  await clickButtonContaining('View import history');
  await waitFor("Boolean(document.querySelector('section[aria-labelledby=" + JSON.stringify('document-imports-title') + "]')?.innerText.includes('meeting-notes.docx'))", Boolean, 'saved friendly filename mapping', 15000);
  await waitFor("document.querySelector('section[aria-labelledby=" + JSON.stringify('document-imports-title') + "]')?.innerText.includes('Converted text was saved as a source')", Boolean, 'import state refreshed to success', 15000);
  const successEvidence = await evaluate("({anchors:[...document.querySelectorAll('section[aria-labelledby=\\\"document-imports-title\\\"] a')].map(x=>x.innerText.trim()),section:document.querySelector('section[aria-labelledby=\\\"document-imports-title\\\"]')?.innerText})");
  check('455-import-success-and-authoritative-name', (successEvidence?.section ?? '').includes('meeting-notes.docx') && (successEvidence?.section ?? '').includes('Converted text was saved as a source'), JSON.stringify(successEvidence));
  await capture('document-import-success-desktop-1280x900.png');
  await navigate('/');
  await navigate('/sources');
  await waitFor("[...document.querySelectorAll('a')].some(x=>x.innerText.trim()==='meeting-notes.docx')", Boolean, 'metadata-backed Source display name after refreshed source list');
  await clickButtonContaining('View import history');
  await waitFor("Boolean(document.querySelector('a[href*=" + JSON.stringify('document_id=fixture-import-1') + "]'))", Boolean, 'metadata-backed source deep link', 15000);
  check('455-friendly-name-and-source-link', await evaluate("[...document.querySelectorAll('a')].some(x=>x.innerText.trim()==='meeting-notes.docx') && Boolean(document.querySelector('a[href*=" + JSON.stringify('document_id=fixture-import-1') + "]'))"), 'The source list and import history map the display name from authoritative original_filename metadata.');

  await changeSelect('section[aria-labelledby="document-imports-title"] select', 'failed');
  await waitFor("Boolean(document.querySelector('#document-imports-title'))", Boolean, 'empty failure filter');
  await waitFor("document.body.innerText.includes('No import records match this filter')", Boolean, 'empty failed state');
  check('455-empty-failure-filter-retains-controls', await evaluate("Boolean(document.querySelector('#document-imports-title')) && Boolean(document.querySelector('select')) && [...document.querySelectorAll('button')].some(x=>x.innerText.includes('import history') || x.innerText.includes('saved history'))"), 'Empty failed result retains status filter and history controls.');
  await setFixture({ includeFailure: true });
  await changeSelect('section[aria-labelledby="document-imports-title"] select', 'all');
  await changeSelect('section[aria-labelledby="document-imports-title"] select', 'failed');
  await waitFor("document.body.innerText.includes('failed-review.docx')", Boolean, 'existing confirmed failed import');
  const sourceTrigger = await evaluate("document.querySelector('button[aria-label*=" + JSON.stringify('raw/review.md') + "]')?.getAttribute('aria-label')");
  if (sourceTrigger) {
    await evaluate(`(() => { const b=document.querySelector('button[aria-label*=${JSON.stringify('raw/review.md')}]');b.focus();b.click(); })()`);
    await waitFor("Boolean(document.querySelector('#source-raw-preview-dialog'))", Boolean, 'Source raw preview dialog');
    await waitFor("Boolean(document.querySelector('#source-raw-preview-dialog article h2'))", Boolean, 'Source raw Markdown content');
    const sourceMarkdown = await evaluate("(() => {const a=document.querySelector('#source-raw-preview-dialog article');const p=a?.querySelector('p');return {leadingH1:a?.querySelector('h1')?.textContent?.trim(),heading:!!a?.querySelector('h2'),list:!!a?.querySelector('ul'),code:!!a?.querySelector('pre code'),paragraphImmediatelyBeforeTable:p?.nextElementSibling?.tagName==='TABLE'};})()");
    check('456-source-preview-markdown-structure', Boolean(sourceMarkdown?.leadingH1 === 'Synthetic source heading' && sourceMarkdown?.heading && sourceMarkdown?.list && sourceMarkdown?.code && sourceMarkdown?.paragraphImmediatelyBeforeTable), JSON.stringify(sourceMarkdown));
    await key('Escape');
    await waitFor("!document.querySelector('#source-raw-preview-dialog')", Boolean, 'Source modal escape');
    check('456-source-preview-focus-return', await evaluate("document.activeElement?.getAttribute('aria-label') === document.querySelector('button[aria-label*=" + JSON.stringify('raw/review.md') + "]')?.getAttribute('aria-label')"), 'Escape returns focus to the source preview trigger.');
  } else {
    check('456-source-preview-trigger', false, 'Source preview action is missing.');
  }
  await capture('failed-import-actions-desktop-1280x900.png');
  await evaluate('window.confirm = () => true');
  await clickButtonContaining('Download original');
  await waitForFixture((state) => state.requests.some((request) => request.method === 'GET' && request.path.endsWith('/fixture-failed-import-1/original')), 'failed original download');
  await clickButtonContaining('Delete failed import');
  await waitFor("!document.body.innerText.includes('failed-review.docx')", Boolean, 'confirmed failed import deletion');
  const afterDelete = await fetch(`${fixture}/__fixture/state`).then((response) => response.json());
  check('455-confirmed-failed-import-download-delete', afterDelete.requests.some((r) => r.method === 'GET' && r.path.endsWith('/fixture-failed-import-1/original')) && afterDelete.requests.some((r) => r.method === 'DELETE' && r.path.endsWith('/fixture-failed-import-1')), 'Only the existing failed-import original download and its confirmed DELETE were exercised.');
  await waitFor("[...document.querySelectorAll('a[href=" + JSON.stringify('/sources') + "]')].some((link)=>link.innerText.includes('2'))", Boolean, 'source count refreshed after import and failed-record deletion');
  await capture('empty-failed-filter-desktop-1280x900.png');

  process.stdout.write('checkpoint: failed import actions complete\n');
  await navigate('/sources');
  await waitFor("Boolean(document.querySelector('a[href*=" + JSON.stringify('/sources/fixture-source-1-review') + "]'))", Boolean, 'source detail link');
  await clickLinkContaining('Review');
  await waitFor("Boolean(document.querySelector('[data-testid=" + JSON.stringify('source-raw-file-link') + "]'))", Boolean, 'source detail Raw file link');
  const openedRaw = await evaluate(`(() => { const link=document.querySelector('[data-testid=${JSON.stringify('source-raw-file-link')}]');if(!link)return false;link.focus();link.click();return true; })()`);
  if (!openedRaw) throw new Error('Source detail Raw file link could not be activated.');
  await waitFor("Boolean(document.querySelector('#raw-preview-dialog'))", Boolean, 'Raw preview dialog');
  await waitFor("Boolean(document.querySelector('#raw-preview-dialog article h2'))", Boolean, 'Raw Markdown content');
  const rawMarkdown = await evaluate("(() => {const a=document.querySelector('#raw-preview-dialog article');const p=a?.querySelector('p');const d=document.querySelector('#raw-preview-dialog');const r=d.getBoundingClientRect();return {leadingH1:a?.querySelector('h1')?.textContent?.trim(),heading:!!a?.querySelector('h2'),list:!!a?.querySelector('ul'),code:!!a?.querySelector('pre code'),paragraphImmediatelyBeforeTable:p?.nextElementSibling?.tagName==='TABLE',bounds:{x:r.x,y:r.y,w:r.width,h:r.height}};})()");
  check('456-raw-preview-markdown-structure', Boolean(rawMarkdown?.leadingH1 === 'Synthetic source heading' && rawMarkdown?.heading && rawMarkdown?.list && rawMarkdown?.code && rawMarkdown?.paragraphImmediatelyBeforeTable), JSON.stringify(rawMarkdown));
  await capture('raw-markdown-preview-desktop-1280x900.png');
  await key('Escape');
  await waitFor("!document.querySelector('#raw-preview-dialog')", Boolean, 'Raw modal escape');
  await waitFor("Boolean(document.querySelector('button[aria-label*=" + JSON.stringify('review.md') + "]'))", Boolean, 'Raw preview return trigger');
  await evaluate(`(() => { const b=document.querySelector('button[aria-label*=${JSON.stringify('review.md')}]');b.focus();b.click(); })()`);
  await waitFor("Boolean(document.querySelector('#raw-preview-dialog'))", Boolean, 'Raw preview dialog reopened from its actual trigger');
  await key('Escape');
  await waitFor("!document.querySelector('#raw-preview-dialog')", Boolean, 'Raw modal return focus');
  check('456-raw-preview-focus-return', await evaluate("document.activeElement?.getAttribute('aria-label') === document.querySelector('button[aria-label*=" + JSON.stringify('review.md') + "]')?.getAttribute('aria-label')"), 'Escape returns focus to the raw preview trigger.');
  process.stdout.write('checkpoint: Raw markdown preview complete\n');

  await navigate('/profile');
  const localeLabel = await evaluate("[...document.querySelectorAll('select')].find((item) => [...item.options].some((option) => option.value === 'zh-TW'))?.getAttribute('aria-label')");
  if (!localeLabel) throw new Error('Locale selector with a Traditional Chinese option is missing.');
  await changeSelect(`select[aria-label=${JSON.stringify(localeLabel)}]`, 'zh-TW');
  await waitFor("[...document.querySelectorAll('select')].some((item) => item.value === 'zh-TW')", Boolean, 'Traditional Chinese locale selection');
  const localeEvidence = await evaluate("({ locale: [...document.querySelectorAll('select')].find((item) => item.value === 'zh-TW')?.value, title: document.querySelector('#project-profile-heading')?.textContent?.trim(), fallbackNotice: [...document.querySelectorAll('[role=status]')].some((item) => item.innerText.includes('Translations currently fall back')) })");
  check('454-zh-tw-runtime-locale', localeEvidence?.locale === 'zh-TW' && localeEvidence?.title === '專案設定' && !localeEvidence?.fallbackNotice, JSON.stringify(localeEvidence));
  await capture('project-settings-zh-TW-runtime-1280x900.png');

  const machine = {
    generated_at: new Date().toISOString(), source_base: '0bf7fd7aca60d0de7b1dbc4299d9a3a905939238',
    fixture: { auth: 'synthetic local storage token/user only', api: 'loopback-only Node fixture', provider_calls: 0, cloud_calls: 0, live_data: false },
    viewport: { settings: settingsLayoutGeometry, project_entry_modal: projectModalGeometry, direct_account_modal: directModalGeometry },
    checks, screenshots: [
      'local-entry-lab-1280x900.png',
      'home-desktop-normal-1280x900.png','project-settings-desktop-normal-1280x900.png','project-settings-desktop-short-1280x577.png','project-settings-mobile-390x844.png','project-settings-zh-TW-fallback-1280x900.png','project-settings-zh-TW-runtime-1280x900.png',
      'project-entry-account-modal-normal-1280x900.png','project-entry-account-modal-short-1280x577.png','project-entry-account-modal-mobile-390x844.png',
      'direct-account-modal-normal-1280x900.png','direct-account-modal-short-1280x577.png','direct-account-modal-mobile-390x844.png',
      'document-import-in-progress-desktop-1280x900.png','document-import-success-desktop-1280x900.png','empty-failed-filter-desktop-1280x900.png','failed-import-actions-desktop-1280x900.png','raw-markdown-preview-desktop-1280x900.png',
    ],
  };
  writeFileSync(resultsPath, `${JSON.stringify(machine, null, 2)}\n`);
  process.stdout.write(`LOCAL UX cases: ${checks.filter((item) => item.passed).length}/${checks.length} passed\nResults: ${resultsPath}\n`);
} catch (error) {
  const fixtureState = await fetch(`${fixture}/__fixture/state`).then((response) => response.json()).catch(() => null);
  writeFileSync(resultsPath, `${JSON.stringify({ generated_at: new Date().toISOString(), source_base: '0bf7fd7aca60d0de7b1dbc4299d9a3a905939238', checks, failure: error instanceof Error ? error.message : String(error), fixture_state: fixtureState }, null, 2)}\n`);
  process.stderr.write(`${error instanceof Error ? error.stack : error}\n`);
  process.exitCode = 1;
} finally {
  socket?.close();
  chrome.kill('SIGTERM');
  await delay(150);
  rmSync(chromeProfile, { recursive: true, force: true });
}

// Run after npm run build: inspect the real prerendered HTML with scripts disabled.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { JSDOM } from 'jsdom';

const manifest = JSON.parse(await readFile(new URL('../.next/prerender-manifest.json', import.meta.url), 'utf8'));
for (const [route, title, disclosure] of [
  ['/privacy', '隱私權政策', '專案內容與 AI 處理'],
  ['/terms', '服務條款', '你提交的內容'],
]) {
  assert.ok(manifest.routes[route], `${route} must be prerendered`);
  assert.equal(manifest.routes[route].initialRevalidateSeconds, false);
  const html = await readFile(new URL(`../.next/server/app${route}.html`, import.meta.url), 'utf8');
  const dom = new JSDOM(html); // No JS execution or resource loading: no login/API can help this pass.
  const doc = dom.window.document;
  assert.equal(doc.querySelector('main h1')?.textContent, title);
  assert.ok(doc.querySelector('main')?.textContent.includes(disclosure));
  assert.ok(doc.querySelector('main')?.textContent.includes('自公開發布時生效'));
  assert.doesNotMatch(doc.documentElement.textContent, /草案|尚未生效|擬議|預設設定|DeepSeek/);
  assert.ok(doc.querySelector('main')?.textContent.includes('由 Rayer Tung 經營的個人興趣專案'));
  assert.ok(doc.querySelector('main')?.textContent.includes('服務、隱私或資料刪除相關請求'));
  assert.equal(doc.querySelector('a[href="mailto:rayershih@gmail.com"]')?.textContent, 'rayershih@gmail.com');
  assert.ok(doc.querySelectorAll('main section').length >= 5);
  assert.equal(doc.title, `${title} | LLM Wiki Cloud`);
  assert.ok(doc.querySelector('meta[name="description"]')?.content.includes('LLM Wiki Cloud'));
  assert.ok(doc.querySelector('meta[name="viewport"]')?.content.includes('width=device-width'));
  const robots = doc.querySelector('meta[name="robots"]')?.content.split(/,\s*/);
  assert.ok(robots?.includes('index'));
  assert.ok(robots?.includes('follow'));
  assert.ok(!robots?.includes('noindex'));
  const body = doc.querySelector('main').textContent;
  if (route === '/privacy') {
    for (const statement of ['在提供服務期間，服務會保留帳號及專案資料', '確認申請人身分後人工處理', '在回覆中說明實際處理範圍與限制', '刪除不保證即時完成', '第三方保留的副本', '服務設定使用的第三方 AI 提供者']) {
      assert.ok(body.includes(statement), `missing privacy disclosure: ${statement}`);
    }
  } else {
    for (const statement of ['你保留所提交內容的原有權利', '在提供服務所必要的範圍內', '請核對原始來源', '不保證持續可用', '請自行備份重要資料', '管理員可限制或停用帳號']) {
      assert.ok(body.includes(statement), `missing terms provision: ${statement}`);
    }
  }
  assert.equal(doc.querySelector('[role="dialog"]'), null);
  for (const target of ['/', '/privacy', '/terms']) {
    assert.ok(doc.querySelector(`a[href="${target}"]`));
  }
  console.log(`${route}: substantive static HTML, metadata and native links verified without JavaScript`);
  dom.window.close();
}

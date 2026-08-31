'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const chatStream = require('../../api/chat-stream.js');
const deepseekConstants = require('../../internal/js/shared/deepseek-constants.js');
const { parseToolCallsDetailed, parseStandaloneToolCallsDetailed } = require('../../internal/js/helpers/stream-tool-sieve.js');

const { parseChunkForContent, estimateTokens } = chatStream.__test;

const compatRoot = path.resolve(__dirname, '../../tests/compat');

function readJSON(filePath) {
  return JSON.parse(fs.readFileSync(filePath, 'utf8'));
}

test('js shared constants derive client headers from shared json', () => {
  const shared = readJSON(path.resolve(__dirname, '../../internal/deepseek/protocol/constants_shared.json'));
  const client = shared.client;
  assert.equal(deepseekConstants.CLIENT_VERSION, client.version);
  assert.equal(deepseekConstants.BASE_HEADERS['x-client-version'], client.version);
  assert.equal(deepseekConstants.BASE_HEADERS['x-client-platform'], 'web');
  // 真实网页版抓包确认 platform=web 同样携带此头。
  assert.equal(deepseekConstants.BASE_HEADERS['x-client-bundle-id'], 'com.deepseek.chat');
  assert.equal(deepseekConstants.BASE_HEADERS['Content-Type'], 'application/json');
  // web 平台应使用 Chrome User-Agent，与 Go 侧 httpcloak 预设同源（见 constants_shared.json）。
  assert.ok(deepseekConstants.BASE_HEADERS['User-Agent'].includes('Chrome'), `unexpected user agent=${deepseekConstants.BASE_HEADERS['User-Agent']}`);
  for (const h of ['sec-ch-ua', 'sec-ch-ua-mobile', 'sec-ch-ua-platform', 'sec-fetch-site', 'sec-fetch-mode', 'sec-fetch-dest', 'Referer', 'Origin', 'Accept-Language']) {
    assert.ok(deepseekConstants.BASE_HEADERS[h], `expected browser header missing: ${h}`);
  }
  for (const h of ['accept-encoding', 'accept-charset']) {
    assert.equal(deepseekConstants.BASE_HEADERS[h], undefined, `unexpected header present: ${h}`);
  }
});

// 跨语言单一来源守卫：Chrome 指纹必须来自 constants_shared.json（Go 侧读同一文件）。
// 谁在 JS 里单独改版本号或 GREASE 串而不改 JSON，本用例直接失败。
test('js shared constants derive Chrome fingerprint from shared json', () => {
  const shared = readJSON(path.resolve(__dirname, '../../internal/deepseek/protocol/constants_shared.json'));
  const chrome = shared.chrome;
  assert.ok(chrome && chrome.major_version, 'chrome.major_version missing from constants_shared.json');
  assert.ok(chrome.grease_brands && Object.keys(chrome.grease_brands).length > 0, 'chrome.grease_brands missing');

  const envMajor = String(process.env.DS2API_CHROME_MAJOR_VERSION || '').trim();
  if (/^\d+$/.test(envMajor) && Number(envMajor) >= 133) {
    assert.equal(deepseekConstants.CHROME_MAJOR_VERSION, envMajor, 'valid env override must win');
    return;
  }
  const major = chrome.major_version;
  assert.equal(deepseekConstants.CHROME_MAJOR_VERSION, major);
  assert.ok(deepseekConstants.CHROME_USER_AGENT.includes(`Chrome/${major}.0.0.0`), `UA=${deepseekConstants.CHROME_USER_AGENT}`);

  const expectedGrease = chrome.grease_brands[major] || chrome.grease_brands[chrome.grease_fallback_major];
  assert.ok(expectedGrease, `no GREASE brand for major=${major} and no fallback`);
  assert.equal(
    deepseekConstants.CHROME_SEC_CH_UA,
    `${expectedGrease}, "Chromium";v="${major}", "Google Chrome";v="${major}"`,
  );
  assert.equal(deepseekConstants.BASE_HEADERS['sec-ch-ua'], deepseekConstants.CHROME_SEC_CH_UA);
  assert.equal(deepseekConstants.BASE_HEADERS['User-Agent'], deepseekConstants.CHROME_USER_AGENT);
});

test('js compat: sse fixtures', () => {
  const fixtureDir = path.join(compatRoot, 'fixtures', 'sse_chunks');
  const expectedDir = path.join(compatRoot, 'expected');
  const files = fs.readdirSync(fixtureDir).filter((f) => f.endsWith('.json')).sort();
  assert.ok(files.length > 0);

  for (const file of files) {
    const name = file.replace(/\.json$/i, '');
    const fixture = readJSON(path.join(fixtureDir, file));
    const expected = readJSON(path.join(expectedDir, `sse_${name}.json`));
    const got = parseChunkForContent(fixture.chunk, Boolean(fixture.thinking_enabled), fixture.current_type || 'text');
    assert.deepEqual(got.parts, expected.parts, `${name}: parts mismatch`);
    assert.equal(got.finished, expected.finished, `${name}: finished mismatch`);
    assert.equal(got.newType, expected.new_type, `${name}: newType mismatch`);
    assert.equal(Boolean(got.contentFilter), Boolean(expected.content_filter), `${name}: contentFilter mismatch`);
    assert.equal(got.errorMessage || '', expected.error_message || '', `${name}: errorMessage mismatch`);
  }
});

test('js compat: toolcall fixtures', () => {
  const fixtureDir = path.join(compatRoot, 'fixtures', 'toolcalls');
  const expectedDir = path.join(compatRoot, 'expected');
  const files = fs.readdirSync(fixtureDir).filter((f) => f.endsWith('.json')).sort();
  assert.ok(files.length > 0);

  for (const file of files) {
    const name = file.replace(/\.json$/i, '');
      const fixture = readJSON(path.join(fixtureDir, file));
      const expected = readJSON(path.join(expectedDir, `toolcalls_${name}.json`));
      const mode = typeof fixture.mode === 'string' ? fixture.mode.trim().toLowerCase() : '';
      const parser = mode === 'standalone' ? parseStandaloneToolCallsDetailed : parseToolCallsDetailed;
      const got = parser(fixture.text, fixture.tool_names || []);
      assert.deepEqual(got.calls, expected.calls, `${name}: calls mismatch`);
      assert.equal(got.sawToolCallSyntax, expected.sawToolCallSyntax, `${name}: sawToolCallSyntax mismatch`);
      assert.equal(got.rejectedByPolicy, expected.rejectedByPolicy, `${name}: rejectedByPolicy mismatch`);
      assert.deepEqual(got.rejectedToolNames, expected.rejectedToolNames, `${name}: rejectedToolNames mismatch`);
    }
  });

test('js compat: token fixtures', () => {
  const fixture = readJSON(path.join(compatRoot, 'fixtures', 'token_cases.json'));
  const expected = readJSON(path.join(compatRoot, 'expected', 'token_cases.json'));
  const expectedByName = new Map(expected.cases.map((c) => [c.name, c.tokens]));
  for (const c of fixture.cases) {
    assert.ok(expectedByName.has(c.name), `missing expected case: ${c.name}`);
    const got = estimateTokens(c.text);
    assert.equal(got, expectedByName.get(c.name), `${c.name}: tokens mismatch`);
  }
});

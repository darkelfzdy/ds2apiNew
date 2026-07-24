'use strict';

const fs = require('fs');
const path = require('path');

const DEFAULT_CLIENT = Object.freeze({
  name: 'DeepSeek',
  platform: 'web',
  androidApiLevel: '',
  locale: 'zh_CN',
});

const DEFAULT_BASE_HEADERS = Object.freeze({
  Host: 'chat.deepseek.com',
  Accept: 'application/json',
  'Content-Type': 'application/json',
});

// chromeMajorVersion 与 Go 侧 transport 层 utls.HelloChrome_Auto 保持一致。
const CHROME_MAJOR_VERSION = '128';
const CHROME_USER_AGENT = `Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/${CHROME_MAJOR_VERSION}.0.0.0 Safari/537.36`;
const CHROME_SEC_CH_UA = `"Not.A/Brand";v="8", "Chromium";v="${CHROME_MAJOR_VERSION}", "Google Chrome";v="${CHROME_MAJOR_VERSION}"`;

const WEB_BROWSER_HEADERS = Object.freeze({
  'User-Agent': CHROME_USER_AGENT,
  'sec-ch-ua': CHROME_SEC_CH_UA,
  'sec-ch-ua-mobile': '?0',
  'sec-ch-ua-platform': '"Windows"',
  Origin: 'https://chat.deepseek.com',
  Referer: 'https://chat.deepseek.com/',
  'sec-fetch-site': 'same-origin',
  'sec-fetch-mode': 'cors',
  'sec-fetch-dest': 'empty',
});

const LOCALE_TIMEZONE_OFFSETS = Object.freeze({
  zh_CN: '28800',
  zh_TW: '28800',
  en_US: '-420',
  en_GB: '3600',
  ja_JP: '32400',
  ko_KR: '32400',
  de_DE: '7200',
  fr_FR: '7200',
  ru_RU: '18000',
  es_ES: '7200',
});

const LOCALE_ACCEPT_LANGUAGES = Object.freeze({
  zh_CN: 'zh-CN,zh;q=0.9',
  zh_TW: 'zh-TW,zh;q=0.9',
  en_US: 'en-US,en;q=0.9',
  en_GB: 'en-GB,en;q=0.9',
  ja_JP: 'ja-JP,ja;q=0.9',
  ko_KR: 'ko-KR,ko;q=0.9',
  de_DE: 'de-DE,de;q=0.9',
  fr_FR: 'fr-FR,fr;q=0.9',
  ru_RU: 'ru-RU,ru;q=0.9',
  es_ES: 'es-ES,es;q=0.9',
});

const DEFAULT_SKIP_PATTERNS = Object.freeze([
  'quasi_status',
  'elapsed_secs',
  'token_usage',
  'pending_fragment',
  'conversation_mode',
  'fragments/-1/status',
  'fragments/-2/status',
  'fragments/-3/status',
]);

const DEFAULT_SKIP_EXACT_PATHS = Object.freeze([
  'response/search_status',
]);

function asNonEmptyString(value) {
  return typeof value === 'string' && value !== '' ? value : '';
}

function normalizeClient(raw) {
  const client = raw && typeof raw === 'object' && !Array.isArray(raw) ? raw : {};
  return {
    name: asNonEmptyString(client.name) || DEFAULT_CLIENT.name,
    platform: asNonEmptyString(client.platform) || DEFAULT_CLIENT.platform,
    version: asNonEmptyString(client.version),
    androidApiLevel: asNonEmptyString(client.android_api_level) || DEFAULT_CLIENT.androidApiLevel,
    locale: asNonEmptyString(client.locale) || DEFAULT_CLIENT.locale,
  };
}

function timezoneOffsetFor(locale) {
  const key = asNonEmptyString(locale);
  return key && LOCALE_TIMEZONE_OFFSETS[key] ? LOCALE_TIMEZONE_OFFSETS[key] : '28800';
}

function acceptLanguageFor(locale) {
  const key = asNonEmptyString(locale);
  return key && LOCALE_ACCEPT_LANGUAGES[key] ? LOCALE_ACCEPT_LANGUAGES[key] : 'zh-CN,zh;q=0.9';
}

function isWebPlatform(platform) {
  return asNonEmptyString(platform).toLowerCase() === 'web';
}

function buildBaseHeaders(parsed, client) {
  const rawBaseHeaders = parsed && typeof parsed.base_headers === 'object' && !Array.isArray(parsed.base_headers)
    ? parsed.base_headers
    : {};
  const baseHeaders = { ...DEFAULT_BASE_HEADERS, ...rawBaseHeaders };

  const locale = client.locale || 'zh_CN';
  baseHeaders['x-client-timezone-offset'] = timezoneOffsetFor(locale);

  if (isWebPlatform(client.platform)) {
    Object.assign(baseHeaders, WEB_BROWSER_HEADERS);
    baseHeaders['Accept-Language'] = acceptLanguageFor(locale);
  } else if (client.name && client.version) {
    baseHeaders['User-Agent'] = `${client.name}/${client.version}`;
  }

  if (client.platform) {
    baseHeaders['x-client-platform'] = client.platform;
  }
  if (client.version) {
    baseHeaders['x-client-version'] = client.version;
  }
  if (client.locale) {
    baseHeaders['x-client-locale'] = client.locale;
  }
  return baseHeaders;
}

function sharedConstantsPaths() {
  return [
    path.resolve(__dirname, '../../deepseek/protocol/constants_shared.json'),
    path.resolve(process.cwd(), 'internal/deepseek/protocol/constants_shared.json'),
  ];
}

function readSharedConstants() {
  try {
    return require('../../deepseek/protocol/constants_shared.json');
  } catch (_err) {
    // Fall through to filesystem candidates for test and local execution variants.
  }
  for (const sharedPath of sharedConstantsPaths()) {
    try {
      const raw = fs.readFileSync(sharedPath, 'utf8');
      return JSON.parse(raw);
    } catch (_err) {
      // Try the next candidate path; fall back to in-file structural defaults below.
    }
  }
  return {};
}

function loadSharedConstants() {
  const parsed = readSharedConstants();
  const client = normalizeClient(parsed && parsed.client);
  const skipPatterns = Array.isArray(parsed && parsed.skip_contains_patterns)
    ? parsed.skip_contains_patterns.filter((v) => typeof v === 'string' && v !== '')
    : [...DEFAULT_SKIP_PATTERNS];
  const skipExactPaths = Array.isArray(parsed && parsed.skip_exact_paths)
    ? parsed.skip_exact_paths.filter((v) => typeof v === 'string' && v !== '')
    : [...DEFAULT_SKIP_EXACT_PATHS];
  return {
    client,
    baseHeaders: buildBaseHeaders(parsed, client),
    skipPatterns,
    skipExactPaths,
  };
}

const shared = loadSharedConstants();

module.exports = {
  CLIENT: Object.freeze({ ...shared.client }),
  CLIENT_VERSION: shared.client.version,
  BASE_HEADERS: Object.freeze(shared.baseHeaders),
  SKIP_PATTERNS: Object.freeze(shared.skipPatterns),
  SKIP_EXACT_PATHS: new Set(shared.skipExactPaths),
  WEB_BROWSER_HEADERS: Object.freeze({ ...WEB_BROWSER_HEADERS }),
  CHROME_USER_AGENT,
  CHROME_SEC_CH_UA,
  timezoneOffsetFor,
  acceptLanguageFor,
  isWebPlatform,
  __test: {
    buildBaseHeaders,
    normalizeClient,
    sharedConstantsPaths,
    timezoneOffsetFor,
    acceptLanguageFor,
  },
};

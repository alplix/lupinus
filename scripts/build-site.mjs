#!/usr/bin/env node
// Builds the project website into docs/ (served by GitHub Pages) from
// site/template.html and the per-language dictionaries in site/i18n/.
//
//   node scripts/build-site.mjs
//
// Every language gets its own real, fully rendered page — /  for English,
// /<code>/ for the rest — because search engines index URLs, not client-side
// language switches. Each page carries its own translated <title> and
// description, a self canonical, hreflang alternates for every language,
// Open Graph tags and schema.org JSON-LD; docs/sitemap.xml lists all of them
// (submit it in Google Search Console / Bing Webmaster Tools — a robots.txt
// under a project-page path is ignored by crawlers, so there is none). CI
// re-runs this script and fails if the committed docs/ output is stale.
//
// Zero dependencies — Node 20+ builtins only.

import { readFileSync, writeFileSync, mkdirSync, readdirSync, rmSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const siteDir = path.join(root, "site");
const docsDir = path.join(root, "docs");

// Where the site is served. Change this (and only this) if it ever moves to a
// custom domain; every absolute URL in the output derives from it.
const SITE_URL = "https://alplix.github.io/lupinus/";
const REPO_URL = "https://github.com/alplix/lupinus";
const DOWNLOAD_BASE = `${REPO_URL}/releases/latest/download/`;

// code, native name, text direction, Open Graph locale
const LANGS = [
  ["en", "English", "ltr", "en_US"], ["tr", "Türkçe", "ltr", "tr_TR"], ["es", "Español", "ltr", "es_ES"],
  ["fr", "Français", "ltr", "fr_FR"], ["de", "Deutsch", "ltr", "de_DE"], ["it", "Italiano", "ltr", "it_IT"],
  ["pt", "Português", "ltr", "pt_BR"], ["ru", "Русский", "ltr", "ru_RU"], ["ar", "العربية", "rtl", "ar_AR"],
  ["zh", "中文", "ltr", "zh_CN"], ["ja", "日本語", "ltr", "ja_JP"], ["ko", "한국어", "ltr", "ko_KR"],
  ["hi", "हिन्दी", "ltr", "hi_IN"], ["nl", "Nederlands", "ltr", "nl_NL"], ["pl", "Polski", "ltr", "pl_PL"],
  ["uk", "Українська", "ltr", "uk_UA"], ["el", "Ελληνικά", "ltr", "el_GR"], ["sv", "Svenska", "ltr", "sv_SE"],
  ["no", "Norsk", "ltr", "nb_NO"], ["da", "Dansk", "ltr", "da_DK"], ["fi", "Suomi", "ltr", "fi_FI"],
  ["cs", "Čeština", "ltr", "cs_CZ"], ["ro", "Română", "ltr", "ro_RO"], ["hu", "Magyar", "ltr", "hu_HU"],
  ["bg", "Български", "ltr", "bg_BG"], ["hr", "Hrvatski", "ltr", "hr_HR"], ["sr", "Српски", "ltr", "sr_RS"],
  ["sk", "Slovenčina", "ltr", "sk_SK"], ["sl", "Slovenščina", "ltr", "sl_SI"], ["he", "עברית", "rtl", "he_IL"],
  ["fa", "فارسی", "rtl", "fa_IR"], ["vi", "Tiếng Việt", "ltr", "vi_VN"], ["th", "ไทย", "ltr", "th_TH"],
  ["id", "Bahasa Indonesia", "ltr", "id_ID"], ["ms", "Bahasa Melayu", "ltr", "ms_MY"],
  ["sw", "Kiswahili", "ltr", "sw_KE"], ["ur", "اردو", "rtl", "ur_PK"], ["bn", "বাংলা", "ltr", "bn_IN"],
];

const esc = (s) =>
  String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

const template = readFileSync(path.join(siteDir, "template.html"), "utf8");
const dicts = {};
for (const [code] of LANGS) {
  const file = path.join(siteDir, "i18n", `${code}.json`);
  if (!existsSync(file)) fail(`missing translation file ${file}`);
  dicts[code] = JSON.parse(readFileSync(file, "utf8"));
}
const en = dicts.en;
for (const [code, d] of Object.entries(dicts)) {
  for (const k of Object.keys(en)) if (!d[k] || !String(d[k]).trim()) fail(`${code}.json: missing or empty "${k}"`);
  for (const k of Object.keys(d)) if (!(k in en)) fail(`${code}.json: unknown key "${k}"`);
}

const urlFor = (code) => (code === "en" ? SITE_URL : `${SITE_URL}${code}/`);
// Path from a page in `from` to the page of `to`, relative, for <select>/links.
const rel = (from, to) => {
  const up = from === "en" ? "" : "../";
  return to === "en" ? (up || "./") : `${up}${to}/`;
};

// Remove previously generated language folders so a deleted language can't linger.
for (const entry of readdirSync(docsDir, { withFileTypes: true })) {
  if (entry.isDirectory() && LANGS.some(([c]) => c === entry.name) && entry.name !== "en") {
    rmSync(path.join(docsDir, entry.name), { recursive: true, force: true });
  }
}

const hreflangs = [
  ...LANGS.map(([c]) => `  <link rel="alternate" hreflang="${c}" href="${urlFor(c)}">`),
  `  <link rel="alternate" hreflang="x-default" href="${urlFor("en")}">`,
].join("\n");

for (const [code, nativeName, dir, ogLocale] of LANGS) {
  const d = dicts[code];
  const isRoot = code === "en";
  const rootPrefix = isRoot ? "" : "../";
  const title = `Lupinus — ${d["hero.tag"]}`;
  const desc = d["hero.lead"];

  const jsonld = JSON.stringify({
    "@context": "https://schema.org",
    "@type": "SoftwareApplication",
    name: "Lupinus",
    alternateName: `Lupinus ${d["hero.tag"]}`,
    description: desc,
    url: urlFor(code),
    inLanguage: code,
    applicationCategory: "NetworkingApplication",
    applicationSubCategory: "Remote desktop client (VNC, RDP)",
    operatingSystem: "Windows, macOS, Linux",
    downloadUrl: `${REPO_URL}/releases/latest`,
    installUrl: `${REPO_URL}/releases/latest`,
    screenshot: `${SITE_URL}screenshots/live-session.png`,
    image: `${SITE_URL}screenshots/live-session.png`,
    license: "https://opensource.org/licenses/MIT",
    isAccessibleForFree: true,
    offers: { "@type": "Offer", price: "0", priceCurrency: "USD" },
    author: { "@type": "Person", name: "Alperen Yavuz", url: "https://github.com/alplix" },
    sameAs: [REPO_URL],
  }).replace(/</g, "\\u003c");

  const langMap = JSON.stringify(
    Object.fromEntries(LANGS.filter(([c]) => c !== "en").map(([c]) => [c, rel(code, c)])),
  );

  const options = LANGS.map(
    ([c, name]) => `        <option value="${rel(code, c)}" data-code="${c}"${c === code ? " selected" : ""}>${esc(name)}</option>`,
  ).join("\n");
  const langlinks = LANGS.map(
    ([c, name]) => `      <a href="${rel(code, c)}" hreflang="${c}" lang="${c}"${c === code ? ' aria-current="true"' : ""}>${esc(name)}</a>`,
  ).join("\n");

  const special = {
    "@lang": code,
    "@dir": dir,
    "@root": rootPrefix,
    "@home": "#top",
    "@title": esc(title),
    "@desc": esc(desc),
    "@canonical": urlFor(code),
    "@siteUrl": SITE_URL,
    "@ogLocale": ogLocale,
    "@hreflangs": hreflangs,
    "@jsonld": jsonld,
    "@langMap": langMap,
    "@options": options,
    "@langlinks": langlinks,
    "@dl": DOWNLOAD_BASE,
  };

  let html = template.replace(/\{\{([^}]+)\}\}/g, (_, key) => {
    if (key.startsWith("@")) {
      if (!(key in special)) fail(`template uses unknown token {{${key}}}`);
      // Values are pre-escaped / pre-serialised where they need it.
      return special[key];
    }
    if (!(key in d)) fail(`template uses unknown translation key {{${key}}}`);
    return esc(d[key]);
  });

  const outDir = isRoot ? docsDir : path.join(docsDir, code);
  mkdirSync(outDir, { recursive: true });
  writeFileSync(path.join(outDir, "index.html"), html);
}

// sitemap.xml with xhtml:link alternates on every URL (Google's recommended
// way to declare language versions in a sitemap).
const alt = LANGS.map(([c]) => `    <xhtml:link rel="alternate" hreflang="${c}" href="${urlFor(c)}"/>`)
  .concat(`    <xhtml:link rel="alternate" hreflang="x-default" href="${urlFor("en")}"/>`)
  .join("\n");
const sitemap =
  `<?xml version="1.0" encoding="UTF-8"?>\n` +
  `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">\n` +
  LANGS.map(
    ([c]) =>
      `  <url>\n    <loc>${urlFor(c)}</loc>\n${alt}\n    <changefreq>monthly</changefreq>\n    <priority>${c === "en" ? "1.0" : "0.8"}</priority>\n  </url>`,
  ).join("\n") +
  `\n</urlset>\n`;
writeFileSync(path.join(docsDir, "sitemap.xml"), sitemap);


console.log(`build-site: wrote ${LANGS.length} pages and sitemap.xml`);

function fail(msg) {
  console.error(`build-site: ${msg}`);
  process.exit(1);
}

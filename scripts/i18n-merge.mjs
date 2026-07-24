// Merge translated strings into web/i18n/<code>.json without disturbing the
// existing entries: the new pairs are inserted textually just before the
// `strings` object closes, matching the files' column-0, one-per-line style.
// Usage: node scripts/i18n-merge.mjs <data.json>
// where data.json is { "<code>": { "<English key>": "<translation>", ... }, ... }
import fs from 'fs';

const data = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
for (const [code, map] of Object.entries(data)) {
  const p = `web/i18n/${code}.json`;
  const raw = fs.readFileSync(p, 'utf8');
  const obj = JSON.parse(raw); // validate + know which keys already exist
  const pairs = Object.entries(map).filter(([k]) => obj.strings[k] === undefined);
  if (!pairs.length) { console.log(code, 'nothing to add'); continue; }
  const m = raw.match(/\}\s*,\s*"help"\s*:/);
  if (!m) { console.error(code, 'ERROR: could not find strings/help boundary'); process.exitCode = 1; continue; }
  const idx = m.index; // position of the } that closes "strings"
  const before = raw.slice(0, idx).replace(/\s*$/, ''); // ...last value, no trailing ws
  const lines = pairs.map(([k, v]) => `${JSON.stringify(k)}: ${JSON.stringify(v)}`);
  const out = before + ',\n' + lines.join(',\n') + '\n' + raw.slice(idx);
  JSON.parse(out); // fail loudly if we produced invalid JSON
  fs.writeFileSync(p, out, 'utf8'); // utf8, no BOM
  console.log(code, 'added', pairs.length);
}

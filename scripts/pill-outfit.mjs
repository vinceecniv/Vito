// Writes internal/pill/outfit.json: the outfit pieces of the web mascot
// (VM_ACC in web/index.html) as plain SVG, for the pill's "vito" style
// (internal/pill/outfit.go), which draws them itself. Run it after changing a
// piece:
//     node scripts/pill-outfit.mjs
import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const html = readFileSync(join(root, "web", "index.html"), "utf8");
const from = html.indexOf("const VM_INK");
const at = html.indexOf("const VM_ACC = {", from);
const end = html.indexOf("\n};", at);
if (from < 0 || at < 0 || end < 0) throw new Error("VM_ACC not found in web/index.html");
const VM_ACC = new Function(html.slice(from, end + 3) + "\nreturn VM_ACC;")();
const out = {};
for (const [k, p] of Object.entries(VM_ACC)) {
  out[k] = { slot: p.slot, space: p.space, svg: p.svg.replace(/\s*\n\s*/g, " ").trim() };
}
writeFileSync(join(root, "internal", "pill", "outfit.json"), JSON.stringify(out, null, 1) + "\n");
console.log(Object.keys(out).length, "pieces");

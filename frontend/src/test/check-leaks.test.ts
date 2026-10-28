import { describe, expect, it } from "vitest";
import { spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

// scripts/check-leaks.mjs runs at the end of `npm run build` over dist/. These run the same script.
const root = resolve(__dirname, "../..");
const run = (...paths: string[]) =>
  spawnSync("node", ["scripts/check-leaks.mjs", ...paths], { cwd: root, encoding: "utf8" });

describe("check-leaks", () => {
  it("fails on a key-shaped string without printing it", () => {
    const dir = mkdtempSync(join(tmpdir(), "leaks-"));
    const key = "AKIA" + "QX7Z".repeat(4); // built here so this file is not key-shaped itself
    writeFileSync(join(dir, "page.js"), `const sample = { key: "${key}", url: "http://lens:8080/v1" };\n`);
    const r = run(dir);
    expect(r.status).toBe(1);
    expect(r.stderr).toContain("page.js:1: AWS access key");
    expect(r.stderr).toContain("page.js:1: internal hostname");
    expect(r.stdout + r.stderr).not.toContain(key);
  });

  it("passes on what the site ships and the built-in page templates", () => {
    const r = run("public", "index.html", "src", "../internal/templatelib");
    expect(r.stderr).toBe("");
    expect(r.status).toBe(0);
  });
});

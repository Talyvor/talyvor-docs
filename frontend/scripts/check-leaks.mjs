// check-leaks.mjs — B50.12: fail the build when anything the docs site ships carries a key-shaped
// string, an internal hostname or address, or someone's email address.
//
//   node scripts/check-leaks.mjs [path ...]     (default: dist, the built site)
//
// A finding prints the file, the line and what kind of string it is — never the string itself, so a
// real key is not copied into the CI log.
import { readFileSync, readdirSync, statSync } from "node:fs";
import { extname, join } from "node:path";

const PATTERNS = [
  ["API key (sk-)", /\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{20,}/],
  ["Stripe key", /\b[rsp]k_(?:live|test)_[A-Za-z0-9]{16,}/],
  ["AWS access key", /\b(?:AKIA|ASIA)[0-9A-Z]{16}\b/],
  ["GitHub token", /\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{20,})/],
  ["Slack token", /\bxox[abprs]-[A-Za-z0-9-]{10,}/],
  ["Google API key", /\bAIza[0-9A-Za-z_-]{35}\b/],
  ["private key", /-----BEGIN [A-Z ]*PRIVATE KEY-----/],
  ["JWT", /\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}/],
  ["bearer token", /\bBearer [A-Za-z0-9._~+/-]{24,}/],
  // In a URL, so a JS property such as `this.internal` is not a host.
  ["internal hostname", /:\/\/(?:[^/\s"'`@]*@)?(?:[a-z0-9-]+\.)+(?:internal|local|corp|lan)\b|:\/\/(?:lens|postgres|db|redis|nats|docs-api|track)(?::\d+)?\//i],
  ["private address", /\b(?:10\.\d{1,3}|192\.168|172\.(?:1[6-9]|2\d|3[01]))\.\d{1,3}\.\d{1,3}\b/],
  // Customer data: any address that is not ours or a reserved example domain.
  ["email address", /\b[A-Za-z0-9._%+-]+@(?!(?:[a-z0-9-]+\.)*(?:talyvor\.com|example\.(?:com|org|net))\b)[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}\b/],
];

const BINARY = new Set([".png", ".jpg", ".jpeg", ".gif", ".ico", ".woff", ".woff2", ".webp"]);

function* files(path) {
  if (statSync(path).isDirectory()) {
    for (const name of readdirSync(path)) {
      // A test file is never shipped, and its fixtures are made up.
      if (name !== "node_modules" && !/[._]test\./.test(name)) yield* files(join(path, name));
    }
  } else if (!BINARY.has(extname(path).toLowerCase())) {
    yield path;
  }
}

const paths = process.argv.slice(2);
let found = 0;
let scanned = 0;
for (const root of paths.length ? paths : ["dist"]) {
  for (const file of files(root)) {
    scanned++;
    readFileSync(file, "utf8")
      .split("\n")
      .forEach((line, i) => {
        for (const [kind, re] of PATTERNS) {
          if (re.test(line)) {
            found++;
            console.error(`${file}:${i + 1}: ${kind}`);
          }
        }
      });
  }
}
console.log(`check-leaks: ${scanned} files, ${found} findings`);
process.exit(found ? 1 : 0);

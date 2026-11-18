#!/usr/bin/env node
// Downloads the Java Support package test/java-plugin.json pins for this machine
// into .artifacts/java-plugin/, checking it against its pinned SHA-256, and
// prints its path. The host end-to-end tests install it: its java tool launches
// Tomcat and its java adapter debugs it. An archive already downloaded and
// matching is kept. The package is a test input only; this plugin never carries it.
//
// Usage: node scripts/test/fetch-java-plugin.mjs [<platform>-<arch>]

import { withInterrupts } from "../process.mjs";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { basename, join, resolve } from "node:path";

const FETCH_TIMEOUT_MS = 300_000;
const pin = JSON.parse(readFileSync("test/java-plugin.json", "utf8"));
const target = process.argv[2] ?? `${process.platform}-${process.arch}`;
const entry = pin.archives[target];
if (!entry) {
  throw new Error(`test/java-plugin.json pins no Java Support archive for ${target}; pinned: ${Object.keys(pin.archives).join(", ")}`);
}
const directory = resolve(".artifacts", "java-plugin");
const archive = join(directory, basename(new URL(entry.url).pathname));
const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");
mkdirSync(directory, { recursive: true });

await withInterrupts(async (signal) => {
  if (existsSync(archive) && sha256(readFileSync(archive)) === entry.sha256) {
    console.error(`✓ Java Support ${pin.version} (${target}) is already downloaded`);
  } else {
    const timeout = AbortSignal.any([signal, AbortSignal.timeout(FETCH_TIMEOUT_MS)]);
    const response = await fetch(entry.url, { signal: timeout });
    if (!response.ok) throw new Error(`GET ${entry.url} answered ${response.status}`);
    const bytes = Buffer.from(await response.arrayBuffer());
    const digest = sha256(bytes);
    if (digest !== entry.sha256) throw new Error(`SHA-256 mismatch for Java Support ${pin.version} (${target}): ${digest}`);
    const partial = `${archive}.partial`;
    writeFileSync(partial, bytes);
    renameSync(partial, archive);
    console.error(`✓ Java Support ${pin.version} (${target}) downloaded and verified`);
  }
  console.log(archive);
});

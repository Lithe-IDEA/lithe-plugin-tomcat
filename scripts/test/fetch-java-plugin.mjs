#!/usr/bin/env node
// Downloads the test inputs test/java-plugin.json pins for the host end-to-end
// tests, checking each against its pinned SHA-256, and prints a path:
//
//   node scripts/test/fetch-java-plugin.mjs [<platform>-<arch>]
//     The Java Support package for this machine (or the named target), in
//     .artifacts/java-plugin/. Its java tool launches Tomcat and its java
//     adapter debugs it.
//   node scripts/test/fetch-java-plugin.mjs --jdtls
//     The Eclipse JDT Language Server Java Support runs, unpacked into
//     .artifacts/jdtls/<version>/, for JDTLS_HOME. Java Support does not carry
//     it; a user installs it.
//
// A download already present and matching is kept. These are test inputs only;
// this plugin never carries them.

import { runCommand, withInterrupts } from "../process.mjs";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { basename, join, resolve } from "node:path";

const FETCH_TIMEOUT_MS = 300_000;
const pin = JSON.parse(readFileSync("test/java-plugin.json", "utf8"));
const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");

/** Downloads url to file unless file already has the pinned digest. */
async function download(url, digest, file, what, signal) {
  if (existsSync(file) && sha256(readFileSync(file)) === digest) {
    console.error(`✓ ${what} is already downloaded`);
    return;
  }
  const timeout = AbortSignal.any([signal, AbortSignal.timeout(FETCH_TIMEOUT_MS)]);
  const response = await fetch(url, { signal: timeout });
  if (!response.ok) throw new Error(`GET ${url} answered ${response.status}`);
  const bytes = Buffer.from(await response.arrayBuffer());
  const actual = sha256(bytes);
  if (actual !== digest) throw new Error(`SHA-256 mismatch for ${what}: ${actual}`);
  writeFileSync(`${file}.partial`, bytes);
  renameSync(`${file}.partial`, file);
  console.error(`✓ ${what} downloaded and verified`);
}

await withInterrupts(async (signal) => {
  if (process.argv[2] === "--jdtls") {
    const { version, url, sha256: digest } = pin.jdtls;
    const directory = resolve(".artifacts", "jdtls");
    const home = join(directory, version);
    mkdirSync(directory, { recursive: true });
    if (!existsSync(join(home, "plugins"))) {
      const archive = join(directory, basename(new URL(url).pathname));
      await download(url, digest, archive, `Eclipse JDT LS ${version}`, signal);
      const partial = join(directory, `.unpack-${version}`);
      rmSync(partial, { recursive: true, force: true });
      mkdirSync(partial);
      await runCommand("tar", ["-xzf", archive, "-C", partial], { timeoutMs: FETCH_TIMEOUT_MS, signal });
      rmSync(home, { recursive: true, force: true });
      renameSync(partial, home);
    }
    console.log(home);
    return;
  }
  const target = process.argv[2] ?? `${process.platform}-${process.arch}`;
  const entry = pin.archives[target];
  if (!entry) {
    throw new Error(`test/java-plugin.json pins no Java Support archive for ${target}; pinned: ${Object.keys(pin.archives).join(", ")}`);
  }
  const directory = resolve(".artifacts", "java-plugin");
  mkdirSync(directory, { recursive: true });
  const archive = join(directory, basename(new URL(entry.url).pathname));
  await download(entry.url, entry.sha256, archive, `Java Support ${pin.version} (${target})`, signal);
  console.log(archive);
});

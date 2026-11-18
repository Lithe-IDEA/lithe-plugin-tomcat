#!/usr/bin/env node
// Downloads the Tomcat versions test/tomcat-versions.json pins into
// .artifacts/tomcat/apache-tomcat-<version>/, checking each archive against
// its pinned SHA-512 before unpacking. A version already unpacked is kept.
// The downloads are test inputs only; the plugin package never carries Tomcat.
//
// Usage: node scripts/test/fetch-tomcat.mjs

import { runCommand, withInterrupts } from "../process.mjs";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";

const FETCH_TIMEOUT_MS = 180_000;
const directory = resolve(".artifacts", "tomcat");
const { versions } = JSON.parse(readFileSync("test/tomcat-versions.json", "utf8"));
mkdirSync(directory, { recursive: true });

await withInterrupts(async (signal) => {
  for (const { version, url, sha512 } of versions) {
    const home = join(directory, `apache-tomcat-${version}`);
    if (existsSync(join(home, "bin", "bootstrap.jar"))) {
      console.log(`✓ Tomcat ${version} is already unpacked`);
      continue;
    }
    const archive = join(directory, `apache-tomcat-${version}.tar.gz`);
    const timeout = AbortSignal.any([signal, AbortSignal.timeout(FETCH_TIMEOUT_MS)]);
    const response = await fetch(url, { signal: timeout });
    if (!response.ok) throw new Error(`GET ${url} answered ${response.status}`);
    const bytes = Buffer.from(await response.arrayBuffer());
    const digest = createHash("sha512").update(bytes).digest("hex");
    if (digest !== sha512) throw new Error(`SHA-512 mismatch for Tomcat ${version}: ${digest}`);
    writeFileSync(archive, bytes);
    const partial = join(directory, `.unpack-${version}`);
    rmSync(partial, { recursive: true, force: true });
    mkdirSync(partial);
    await runCommand("tar", ["-xzf", archive, "-C", partial], { timeoutMs: FETCH_TIMEOUT_MS, signal });
    rmSync(home, { recursive: true, force: true });
    renameSync(join(partial, `apache-tomcat-${version}`), home);
    rmSync(partial, { recursive: true, force: true });
    console.log(`✓ Tomcat ${version} downloaded, verified and unpacked`);
  }
});

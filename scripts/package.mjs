#!/usr/bin/env node
// Packages the plugin into dist/tomcat-<version>-<platform>.zip, the asset
// names Lithe Store's release tracker recognizes.
//
// The package is plugin.json, its translations and one jar: the build
// provider in provider/, compiled with javac for Java 8 together with the
// vendored Java plugin SDK. It carries no native code and no Tomcat, so one
// archive serves every platform; the Linux and Windows assets are copies.
// Packaging needs POSIX zip, so it runs on macOS or Linux. The unpacked
// package stays in dist/package/ for installing a development copy.
//
// Usage: node scripts/package.mjs [--expect-tag <tag>]

import { runCommand, withInterrupts, ZIP_TIMEOUT_MS } from "./process.mjs";
import { verifySdk } from "./sdk/verify-sdk.mjs";
import { createReport } from "./lib/report.mjs";
import { createHash } from "node:crypto";
import { cpSync, lstatSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";

const PACKAGE_DIR = "io.github.lithe-idea.tomcat";
const ASSET_PREFIX = "tomcat";
const PROVIDER_JAR = "lithe-tomcat-provider.jar";
const MAIN_CLASS = "io.github.litheidea.tomcat.TomcatProvider";
const BUILD_TIMEOUT_MS = 120_000;
export const PACKAGE_ROOT = join("dist", "package");

const manifest = JSON.parse(readFileSync(`${PACKAGE_DIR}/plugin.json`, "utf8"));
const problems = [];
if (manifest.id !== PACKAGE_DIR) problems.push(`plugin.json id ${manifest.id} must equal the directory ${PACKAGE_DIR}`);
if (!/^[0-9]+\.[0-9]+\.[0-9]+$/.test(manifest.version)) problems.push(`version ${manifest.version} is not MAJOR.MINOR.PATCH`);
if (manifest.vendor?.signatureRequirement !== "none") problems.push('vendor.signatureRequirement must be "none"');
if (manifest.buildSystems?.[0]?.provider?.launch?.entry !== `lib/${PROVIDER_JAR}`) {
  problems.push(`the provider launch entry must be lib/${PROVIDER_JAR}`);
}
const tagFlag = process.argv.indexOf("--expect-tag");
if (tagFlag >= 0) {
  const tag = process.argv[tagFlag + 1];
  if (tag !== `v${manifest.version}`) problems.push(`tag ${tag} does not match plugin.json version ${manifest.version}`);
}
if (problems.length > 0) {
  for (const problem of problems) console.error(`✗ ${problem}`);
  process.exit(1);
}

const sha256 = (path) => createHash("sha256").update(readFileSync(path)).digest("hex");
const sources = (path) => readdirSync(path, { withFileTypes: true })
  .flatMap((entry) => entry.isDirectory() ? sources(join(path, entry.name)) : entry.name.endsWith(".java") ? [join(path, entry.name)] : [])
  .sort();

/** Lithe refuses packages with symbolic links, so refuse them before zipping. */
function assertNoLinks(directory) {
  for (const name of readdirSync(directory)) {
    const path = join(directory, name);
    const stat = lstatSync(path);
    if (stat.isSymbolicLink()) throw new Error(`${path} is a symbolic link`);
    if (stat.isDirectory()) assertNoLinks(path);
  }
}

const report = createReport("package");
await withInterrupts(async (signal) => {
  try {
    await report.step("verify the vendored SDK", () => verifySdk());
    await report.step("build the provider jar", async () => {
      const build = "provider/target";
      rmSync(build, { recursive: true, force: true });
      mkdirSync(join(build, "classes"), { recursive: true });
      await runCommand("javac", ["--release", "8", "-encoding", "UTF-8", "-Xlint:all,-options", "-Werror", "-d", join(build, "classes"),
        ...sources("provider/src/main/java"), ...sources("vendor/lithe-plugin-sdk-java/src/main/java")], { timeoutMs: BUILD_TIMEOUT_MS, signal });
      writeFileSync(join(build, "MANIFEST.MF"), `Main-Class: ${MAIN_CLASS}\nImplementation-Version: ${manifest.version}\n`);
      await runCommand("jar", ["--create", "--file", join(build, PROVIDER_JAR), "--manifest", join(build, "MANIFEST.MF"),
        "--date", "2026-11-18T00:00:00Z", "-C", join(build, "classes"), "."], { timeoutMs: BUILD_TIMEOUT_MS, signal });
    });
    await report.step("assemble the plugin package", () => {
      signal.throwIfAborted();
      rmSync("dist", { recursive: true, force: true });
      const packageRoot = join(PACKAGE_ROOT, PACKAGE_DIR);
      cpSync(PACKAGE_DIR, packageRoot, { recursive: true });
      mkdirSync(join(packageRoot, "lib"), { recursive: true });
      cpSync(join("provider/target", PROVIDER_JAR), join(packageRoot, "lib", PROVIDER_JAR));
      cpSync("vendor/LICENSE", join(packageRoot, "SDK-LICENSE"));
      cpSync("LICENSE", join(packageRoot, "LICENSE"));
      cpSync("NOTICE", join(packageRoot, "NOTICE"));
      assertNoLinks(packageRoot);
    });
    const asset = resolve("dist", `${ASSET_PREFIX}-${manifest.version}-darwin.zip`);
    // -X drops extra file attributes; -y keeps a link a link so Lithe refuses it.
    await report.step("archive the plugin package", () => runCommand("zip", ["-qrXy", asset, PACKAGE_DIR, "-x", "*.DS_Store"], {
      cwd: PACKAGE_ROOT, timeoutMs: ZIP_TIMEOUT_MS, signal,
    }));
    console.log(`dist/${ASSET_PREFIX}-${manifest.version}-darwin.zip ${sha256(asset)}`);
    for (const platform of ["linux", "windows"]) {
      const copy = resolve("dist", `${ASSET_PREFIX}-${manifest.version}-${platform}.zip`);
      cpSync(asset, copy);
      console.log(`dist/${ASSET_PREFIX}-${manifest.version}-${platform}.zip ${sha256(copy)}`);
    }
  } catch (error) {
    report.failure("packaging failed", error);
    console.error(error.stack);
    process.exitCode = 1;
  } finally {
    report.write();
  }
});

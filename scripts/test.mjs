#!/usr/bin/env node
// End-to-end test of the packaged plugin against real Tomcat installations.
//
// For each Tomcat home it starts the packaged build provider the way Lithe
// does, asks it for the model of every fixture instance, prepares a run of
// each Web module, starts the returned launch plan with `java` (as Lithe
// starts it with the project's JDK) and reads the page over HTTP. It also
// checks the debug launch (a JDWP handshake on the host's port), the
// errors a user can meet, that an edited JSP is served without a new
// deployment, and that neither the plugin package nor the Tomcat
// installation changed.
//
// Inputs:
//   dist/package/                 built by scripts/package.mjs
//   LITHE_TEST_TOMCAT_HOMES       Tomcat homes separated by the path delimiter;
//                                 default: every .artifacts/tomcat/apache-tomcat-*
//   GRADLE_HOME or gradle on PATH; default: .artifacts/gradle/gradle-*
//
// Every wait is bounded and every process the test starts is stopped in a
// finally block, also after a failure.

import { BackendClient, bounded } from "./lib/backend-client.mjs";
import { createReport } from "./lib/report.mjs";
import { withInterrupts } from "./process.mjs";
import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { cpSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import http from "node:http";
import net from "node:net";
import { delimiter, join, relative, resolve } from "node:path";

const PACKAGE_DIR = "io.github.lithe-idea.tomcat";
const pluginDir = resolve("dist", "package", PACKAGE_DIR);
const providerJar = join(pluginDir, "lib", "lithe-tomcat-provider.jar");
const BUILD_MS = 300_000;
const START_MS = 60_000;
const STOP_MS = 10_000;
const WINDOWS = process.platform === "win32";

if (!existsSync(providerJar)) {
  console.error("✗ dist/package is missing; run node scripts/package.mjs first");
  process.exit(1);
}

const homes = (process.env.LITHE_TEST_TOMCAT_HOMES
  ? process.env.LITHE_TEST_TOMCAT_HOMES.split(delimiter)
  : readdirSync(".artifacts/tomcat", { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && entry.name.startsWith("apache-tomcat-"))
    .map((entry) => join(".artifacts/tomcat", entry.name)))
  .filter(Boolean).map((home) => resolve(home));
if (homes.length === 0) {
  console.error("✗ no Tomcat installation: set LITHE_TEST_TOMCAT_HOMES or unpack one under .artifacts/tomcat");
  process.exit(1);
}

function gradleHome() {
  if (process.env.GRADLE_HOME) return process.env.GRADLE_HOME;
  if (existsSync(".artifacts/gradle")) {
    const local = readdirSync(".artifacts/gradle").find((name) => /^gradle-[0-9.]+$/.test(name));
    if (local) return resolve(".artifacts/gradle", local);
  }
  return undefined;
}

const gradle = gradleHome();
// Builds use their own Gradle user home, so the test neither reads nor leaves
// daemons in the user's ~/.gradle; stopGradle() ends the daemons it started.
const gradleEnvironment = {
  GRADLE_USER_HOME: resolve(".artifacts", "test-work", "gradle-user-home"),
  ...(gradle ? { GRADLE_HOME: gradle } : {}),
};

function stopGradle() {
  const command = gradle ? join(gradle, "bin", WINDOWS ? "gradle.bat" : "gradle") : WINDOWS ? "gradle.bat" : "gradle";
  try {
    execFileSync(WINDOWS ? process.env.ComSpec ?? "cmd.exe" : command, WINDOWS ? ["/d", "/c", command, "--stop", "-q"] : ["--stop", "-q"], {
      env: { ...process.env, ...gradleEnvironment }, timeout: 60_000, stdio: "inherit", windowsHide: true,
    });
  } catch (error) {
    console.error(`cannot stop the test's Gradle daemons: ${error.message}`);
    failed = true;
  }
}

const report = createReport("tomcat");
let failed = false;
const check = async (name, body) => {
  try {
    await report.step(name, body);
  } catch (error) {
    failed = true;
    console.error(error.stack);
  }
};

/** A content hash of every file under `directory`, to prove nothing wrote there. */
function treeHash(directory) {
  const hash = createHash("sha256");
  const walk = (path) => {
    for (const entry of readdirSync(path, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
      const child = join(path, entry.name);
      hash.update(relative(directory, child));
      if (entry.isDirectory()) walk(child);
      else hash.update(readFileSync(child));
    }
  };
  walk(directory);
  return hash.digest("hex");
}

function freePort() {
  return new Promise((resolvePort, reject) => {
    const server = net.createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      server.close(() => resolvePort(port));
    });
  });
}

function get(url) {
  return new Promise((resolveBody, reject) => {
    const request = http.get(url, (response) => {
      let body = "";
      response.setEncoding("utf8");
      response.on("data", (chunk) => { body += chunk; });
      response.on("end", () => resolveBody({ status: response.statusCode, body }));
    });
    request.setTimeout(5_000, () => request.destroy(new Error(`GET ${url} timed out`)));
    request.once("error", reject);
  });
}

/** Polls `probe` until it returns a value, for at most `timeoutMs`. */
async function waitFor(description, timeoutMs, probe) {
  const deadline = Date.now() + timeoutMs;
  let last;
  while (Date.now() < deadline) {
    try {
      const value = await probe();
      if (value !== undefined) return value;
    } catch (error) {
      last = error;
    }
    await new Promise((done) => setTimeout(done, 250));
  }
  throw new Error(`timed out after ${timeoutMs} ms waiting for ${description}${last ? `: ${last.message}` : ""}`);
}

/** The JDWP handshake: the debuggee answers the same 14 bytes. */
function jdwpHandshake(port) {
  return new Promise((resolveReply, reject) => {
    const socket = net.connect(port, "127.0.0.1");
    let reply = "";
    socket.setTimeout(5_000, () => socket.destroy(new Error("JDWP handshake timed out")));
    socket.once("connect", () => socket.write("JDWP-Handshake"));
    socket.on("data", (chunk) => {
      reply += chunk.toString("latin1");
      if (reply.length >= 14) {
        socket.destroy();
        resolveReply(reply.slice(0, 14));
      }
    });
    socket.once("error", reject);
  });
}

/** Starts a launch plan the way Lithe does and owns the process until `stop`. */
class Server {
  output = "";

  constructor(workspace, plan) {
    assert.deepEqual(plan.executable, { kind: "toolchain", value: "java" });
    this.child = spawn("java", plan.arguments, {
      cwd: join(workspace, plan.workingDirectory), env: { ...process.env, ...plan.environment },
      stdio: ["ignore", "pipe", "pipe"], detached: !WINDOWS, windowsHide: true,
    });
    const keep = (chunk) => { this.output = (this.output + chunk.toString("utf8")).slice(-64 * 1024); };
    this.child.stdout.on("data", keep);
    this.child.stderr.on("data", keep);
    this.exited = new Promise((done) => this.child.once("exit", done));
  }

  async stop() {
    if (this.child.exitCode !== null || this.child.signalCode !== null || !this.child.pid) return;
    if (WINDOWS) {
      try { execFileSync("taskkill.exe", ["/PID", String(this.child.pid), "/T", "/F"], { timeout: STOP_MS, stdio: "ignore" }); }
      catch (error) { if (error.status !== 128) throw error; }
    } else {
      try { process.kill(-this.child.pid, "SIGTERM"); } catch (error) { if (error.code !== "ESRCH") throw error; }
      const stopped = await Promise.race([this.exited.then(() => true), new Promise((done) => setTimeout(() => done(false), STOP_MS))]);
      if (!stopped) {
        try { process.kill(-this.child.pid, "SIGKILL"); } catch (error) { if (error.code !== "ESRCH") throw error; }
      }
    }
    await bounded(this.exited, "Tomcat to exit", STOP_MS);
  }
}

async function suite(home, signal) {
  const version = /apache-tomcat-([0-9.]+)/.exec(home)?.[1] ?? "custom";
  const work = resolve(".artifacts", "test-work", version);
  rmSync(work, { recursive: true, force: true });
  const workspace = join(work, "workspace");
  const state = join(work, "state");
  mkdirSync(state, { recursive: true });
  cpSync("test/fixtures/workspace", workspace, { recursive: true });
  // The Java plugin compiles an Eclipse project into its output folder; do it here.
  mkdirSync(join(workspace, "wtp", "build", "classes"), { recursive: true });
  execFileSync("javac", ["-source", "8", "-target", "8", "-nowarn", "-d", join(workspace, "wtp", "build", "classes"),
    join(workspace, "wtp", "src", "demo", "wtp", "Message.java")], { timeout: 60_000 });

  const homeBefore = treeHash(home);
  const port = await freePort();
  const environment = { ...process.env, ...gradleEnvironment };
  delete environment.CATALINA_HOME;
  delete environment.CATALINA_BASE;
  const client = new BackendClient({ jar: providerJar, state, env: environment, timeoutMs: BUILD_MS });
  const servers = [];
  let operation = 0;
  const prepare = (root, project, target, launchOptions = {}) => client.request("lithe/build/prepareRun", {
    operationID: `run-${++operation}`, buildSystemID: "tomcat", root, options: {}, optionArguments: [], environment: {},
    run: "tomcat", project: { name: target.name, path: project }, target, userArguments: [],
    launchOptions: { execution: "service", runtimeArguments: [], arguments: [], environment: {}, ...launchOptions },
  });
  const serve = async (plan) => {
    const server = new Server(workspace, plan);
    servers.push(server);
    return server;
  };
  const page = (server, path) => waitFor(`http://127.0.0.1:${port}${path}`, START_MS, async () => {
    if (server.child.exitCode !== null) throw new Error(`Tomcat exited with ${server.child.exitCode}:\n${server.output}`);
    const response = await get(`http://127.0.0.1:${port}${path}`);
    return response.status === 200 ? response.body.trim() : undefined;
  }).catch((error) => { throw new Error(`${error.message}\nTomcat output:\n${server.output}`); });

  try {
    signal.throwIfAborted();
    await report.step(`[${version}] start the build provider`, () => client.initialize(workspace, {
      initializationOptions: { tomcatHome: home, httpPort: port }, locale: "en",
    }));
    const models = {};
    for (const root of ["maven", "wtp", "plain/web/WEB-INF", "gradle-app"]) {
      models[root] = await report.step(`[${version}] model of ${root}`, () => client.request("lithe/build/model", {
        buildSystemID: "tomcat", root, options: {}, optionArguments: [], environment: {},
      }));
    }
    const target = (root) => models[root].targets[0];
    await check(`[${version}] the model lists each Web module and nothing else`, () => {
      const summary = Object.fromEntries(Object.entries(models).map(([root, model]) => [root,
        model.targets.map(({ project, name, tags, properties }) => ({ project, name, tags, ...properties }))]));
      assert.deepEqual(summary, {
        maven: [{ project: "web", name: "shop", tags: ["web-application"], kind: "maven", contextPath: "/shop" }],
        wtp: [{ project: ".", name: "wtp-demo", tags: ["web-application"], kind: "eclipse", contextPath: "/eclipse/demo", webRoot: "WebContent" }],
        "plain/web/WEB-INF": [{ project: ".", name: "plain", tags: ["web-application"], kind: "plain", contextPath: "/plain", webRoot: ".." }],
        "gradle-app": [{ project: ".", name: "gradle-app", tags: ["web-application"], kind: "gradle", contextPath: "/gradle-app" }],
      });
      assert.deepEqual(models.maven.projects.map((project) => project.path), ["web"], "the jar modules are not Web modules");
    });
    await check(`[${version}] an unreadable build file is reported once as a degraded model, and model successes stay quiet`, () => {
      const model = client.diagnostics.filter((fact) => fact.operation === "lithe/build/model");
      assert.deepEqual(model.filter((fact) => fact.kind === "degraded").map(({ code, count }) => ({ code, count })),
        [{ code: "buildFileUnreadable", count: 1 }], JSON.stringify(model));
      assert.equal(model.filter((fact) => fact.kind === "operationFinished").length, 0, "model reads are routine");
    });

    const maven = await report.step(`[${version}] prepare the Maven war module`, () => prepare("maven", "maven/web", target("maven")));
    await check(`[${version}] the Maven module builds with its reactor and serves the exploded WAR`, async () => {
      assert.equal(maven.workingDirectory, "maven/web");
      assert.ok(maven.arguments.includes("org.apache.catalina.startup.Bootstrap"));
      assert.match(maven.environment.JDK_JAVA_OPTIONS, /--add-opens=java.base\/java.lang=ALL-UNNAMED/);
      const base = maven.arguments.find((argument) => argument.startsWith("-Dcatalina.base=")).slice("-Dcatalina.base=".length);
      assert.ok(base.startsWith(state), "CATALINA_BASE lives in the workspace state directory");
      const context = readFileSync(join(base, "conf", "Catalina", "localhost", "shop.xml"), "utf8");
      assert.ok(context.includes(join(workspace, "maven", "web", "target", "shop-1.0")), context);
      const server = await serve(maven);
      assert.equal(await page(server, "/shop/"), "Hello from the Maven reactor");
      await server.stop();
      assert.match(server.output, /Server startup in/, "Tomcat logs to the console Lithe shows");
      assert.ok(!existsSync(join(base, "logs")) || readdirSync(join(base, "logs")).length === 0, "Tomcat writes no log files into the state");
    });

    const wtp = await report.step(`[${version}] prepare the Eclipse Dynamic Web Project`, () => prepare("wtp", "wtp", target("wtp")));
    await check(`[${version}] the Eclipse project is served in place with its output folder, and a JSP edit needs no deployment`, async () => {
      const server = await serve(wtp);
      assert.equal(await page(server, "/eclipse/demo/"), "Hello from the Eclipse output folder");
      writeFileSync(join(workspace, "wtp", "WebContent", "index.jsp"),
        '<%@ page contentType="text/plain; charset=UTF-8" %>edited <%= demo.wtp.Message.text() %>\n');
      await waitFor("the edited JSP", 30_000, async () => {
        const response = await get(`http://127.0.0.1:${port}/eclipse/demo/`);
        return response.body.trim().startsWith("edited ") ? true : undefined;
      });
      await server.stop();
    });

    const plain = await report.step(`[${version}] prepare the plain Web folder`, () => prepare("plain/web/WEB-INF", "plain/web/WEB-INF", target("plain/web/WEB-INF")));
    await check(`[${version}] a plain Web folder found by its web.xml is served in place`, async () => {
      assert.equal(plain.workingDirectory, "plain/web/WEB-INF");
      const server = await serve(plain);
      assert.equal(await page(server, "/plain/"), "Hello from a plain Web folder");
      await server.stop();
    });

    await check(`[${version}] the Gradle war module builds with the war task and serves the WAR`, async () => {
      const plan = await prepare("gradle-app", "gradle-app", target("gradle-app"));
      const server = await serve(plan);
      assert.equal(await page(server, "/gradle-app/"), "Hello from a Gradle WAR");
      await server.stop();
    });

    await check(`[${version}] a debug run suspends Tomcat until a debugger attaches on the host's port`, async () => {
      const debugPort = await freePort();
      const plan = await prepare("wtp", "wtp", target("wtp"), {
        debug: { adapterID: "dev.example/java", configurationType: "java", target: { kind: "connection", host: "127.0.0.1", port: debugPort } },
      });
      const agent = plan.arguments.findIndex((argument) => argument.startsWith("-agentlib:jdwp="));
      assert.equal(plan.arguments[agent], `-agentlib:jdwp=transport=dt_socket,server=y,suspend=y,address=127.0.0.1:${debugPort}`);
      assert.ok(agent < plan.arguments.indexOf("org.apache.catalina.startup.Bootstrap"));
      const server = await serve(plan);
      const reply = await waitFor("the JDWP listener", START_MS, () => jdwpHandshake(debugPort));
      assert.equal(reply, "JDWP-Handshake");
      await assert.rejects(get(`http://127.0.0.1:${port}/eclipse/demo/`), "a suspended Tomcat serves nothing");
      await server.stop();
    });

    await check(`[${version}] a busy HTTP port fails before anything is built`, async () => {
      const blocker = net.createServer();
      await new Promise((done) => blocker.listen(port, "127.0.0.1", done));
      try {
        await assert.rejects(prepare("maven", "maven/web", target("maven")), (error) => {
          assert.match(error.message, new RegExp(`HTTP port ${port} is in use`));
          return true;
        });
      } finally {
        await new Promise((done) => blocker.close(done));
      }
    });

    await check(`[${version}] program arguments and stale targets are refused`, async () => {
      await assert.rejects(prepare("wtp", "wtp", target("wtp"), { arguments: ["--debug"] }), /takes no program arguments/);
      await assert.rejects(prepare("maven", "maven/library", { ...target("maven"), name: "library" }), /no longer a Web module/);
    });

    await check(`[${version}] each run preparation ends with exactly one terminal log fact`, () => {
      const facts = client.diagnostics.filter((fact) => fact.operation === "lithe/build/prepareRun"
        && (fact.kind === "operationFinished" || fact.kind === "operationFailed"));
      assert.equal(facts.length, operation, JSON.stringify(facts));
      assert.deepEqual([...new Set(facts.filter((fact) => fact.kind === "operationFailed").map((fact) => fact.code))].sort(),
        ["tomcat.run.notWebModule", "tomcat.run.portInUse", "tomcat.run.unsupportedArguments"]);
      for (const fact of client.diagnostics) {
        assert.ok(!JSON.stringify(fact).includes(workspace), "log facts carry no paths");
      }
    });

    await check(`[${version}] the provider stops cleanly`, async () => assert.equal(await client.close(), 0));
  } finally {
    for (const server of servers) await server.stop().catch((error) => console.error(error.stack));
    client.kill();
  }
  await check(`[${version}] the Tomcat installation is unchanged`, () => assert.equal(treeHash(home), homeBefore));
}

async function missingHome() {
  const workspace = resolve(".artifacts", "test-work", "no-home");
  rmSync(workspace, { recursive: true, force: true });
  cpSync("test/fixtures/workspace/plain", workspace, { recursive: true });
  const environment = { ...process.env };
  delete environment.CATALINA_HOME;
  const client = new BackendClient({ jar: providerJar, state: join(workspace, ".state"), env: environment });
  try {
    await client.initialize(workspace, { initializationOptions: { tomcatHome: "", httpPort: 8080 }, locale: "zh-Hans" });
    const model = await client.request("lithe/build/model", { buildSystemID: "tomcat", root: "web/WEB-INF", options: {}, optionArguments: [], environment: {} });
    await assert.rejects(client.request("lithe/build/prepareRun", {
      operationID: "run-1", buildSystemID: "tomcat", root: "web/WEB-INF", options: {}, optionArguments: [], environment: {},
      run: "tomcat", project: { name: "plain", path: "web/WEB-INF" }, target: model.targets[0], userArguments: [],
      launchOptions: { execution: "service", runtimeArguments: [], arguments: [], environment: {} },
    }), /没有设置 Tomcat/);
    assert.equal(await client.close(), 0);
  } finally {
    client.kill();
  }
}

const packageBefore = treeHash(pluginDir);
await withInterrupts(async (signal) => {
  try {
    for (const home of homes) {
      assert.ok(statSync(join(home, "bin", "bootstrap.jar")).isFile(), `${home} is not a Tomcat installation`);
      await suite(home, signal);
    }
    await check("without a Tomcat home the run fails with the localized setup message", missingHome);
    await check("the manifest declares settingRegions search keywords and Chinese translations", () => {
      const manifest = JSON.parse(readFileSync(join(pluginDir, "plugin.json"), "utf8"));
      const buildRegion = (manifest.settingRegions ?? []).find((region) => region.id === "build");
      assert.equal(buildRegion?.page?.buildSystemID, "tomcat");
      assert.ok(Array.isArray(buildRegion?.keywords) && buildRegion.keywords.length > 0);

      const zh = JSON.parse(readFileSync(join(pluginDir, "l10n", "zh-Hans.json"), "utf8"));
      assert.ok(zh.sections?.Tomcat, "zh-Hans sections.Tomcat");
      assert.ok(Array.isArray(zh.settingRegions?.build?.keywords) && zh.settingRegions.build.keywords.length > 0);
    });
    await check("the plugin package is unchanged", () => assert.equal(treeHash(pluginDir), packageBefore));
  } catch (error) {
    failed = true;
    report.failure("test lane failed", error);
    console.error(error.stack);
  } finally {
    stopGradle();
    report.write();
  }
});
if (failed) process.exitCode = 1;

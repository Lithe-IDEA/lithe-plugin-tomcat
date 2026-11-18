# Tomcat for Lithe — 0.2.0 (pre-release candidate)

An **independent, on-demand Tomcat integration** for Lithe-IDEA's Viva desktop. It uses the host-owned Run/Debug service and `java` tool from [Java Support](https://github.com/Lithe-IDEA/lithe-plugin-java); neither Lithe's host nor Core knows about Tomcat-specific paths or settings.

> This is a community contribution prepared for review. It has passed local protocol, Go race, JVM and browser-mock tests, **but has not been verified against a native Lithe window or an actual Apache Tomcat distribution**. Do not publish it as a stable Store release until those checks pass.

## Feature coverage

| Feature | 0.2.0 behavior |
| --- | --- |
| Project/Web Facet discovery | Read-only bounded discovery of Maven/Gradle web layouts (up to three nested module-directory levels), Eclipse WTP `.settings` web facets/component resources and IntelliJ `.iml` Web facets; finds WAR and exploded outputs. It skips build/source/dependency trees (256 directory / 20,000 entry scan budget) and **never** rewrites IDE files. |
| Project SDK, classpath, libraries | Remain in the existing **Java Support 4.x Project Structure** page (Sources / Paths / Dependencies, per-module JRE, library scope). Tomcat does not duplicate or override JDT's authoritative classpath. |
| Tomcat installation | Explicit Catalina Home and optional Catalina Base; verifies Bootstrap and JULI JARs, config files, and real application base. Reads `server.xml` Host `appBase`/`autoDeploy` and HTTP connector ports without writing it. |
| Run / Stop | Starts `org.apache.catalina.startup.Bootstrap` with the **JDK selected by Lithe** and host-owned Run lifecycle. No shell or arbitrary scripts, and no bundled Tomcat/JDK. |
| Debug | Runs Tomcat with JDWP bound to `127.0.0.1`, and uses the public `java` debugger adapter alias through Lithe's `run/start.debug` connection target. Needs Java Support installed and the host to expose its Java Debug adapter. |
| WAR artifact deployment | Preview and explicit confirmation before copying to `<appBase>/<context>.war`. ZIP entry count, expanded size, path traversal and symlinks are checked; source/target digests bind confirmation. |
| Exploded Web artifact deployment | Preview and explicit confirmation before copying a workspace directory to `<appBase>/<context>`; bounded file count, size and depth, no symlinks or `.git`/`.idea` trees, source/target digests, replacement backup. Conflicting `<context>.war` blocks exploded deployment. |
| Live WAR deployment | **Opt-in** only. First deploy manually and explicitly confirm, then start a Lithe-managed Tomcat with `server.xml` showing `autoDeploy=true`. A bounded watcher detects a stable changed source WAR and atomically redeploys it; an externally modified target disables the watcher instead of overwriting. Explicit manual redeploy while running follows the same gate. |
| Build integration | Existing Maven/Gradle `package`/`war` tasks produce artifacts. This plugin does not execute arbitrary project build scripts or invent its own Java classpath. |

## Installation prerequisites

- Current Lithe preview supporting `backend:0`, `ui:0`, `workspace.metadata`, `run.start`, plugin WebBridge and the public Debug adapter registry. The host must advertise `litheLogging:0` and `litheFunctions:0` for the backend.
- The separately installed **Java Support** plugin providing the `java` tool ID and Java Debug adapter. Configure the project JDK in Lithe's toolchains; JDT LS's JDK can be different.
- A local Tomcat Home (Tomcat 9/10 deployment layouts are supported conceptually). A compatible JDK and usable application base are the user's responsibility. **External and already-running Tomcat instances are not managed**.
- A **single local workspace root** (multi-module projects within it are supported for Web facet discovery). Remote workspace roots are unsupported.

## Walkthrough

1. Install the platform-matched ZIP from the Release assets or local build. Open a local Java Web workspace and the **Tomcat** tool window.
2. In **Web modules / Facets**, inspect discovered modules and output candidates. Click **Use WAR / Use Web** to populate the artifact. Non-build project Web roots may appear as exploded candidates, but they need compiled `WEB-INF/classes` and libraries to run correctly; building is still your responsibility.
3. Set Tomcat Home, optionally Catalina Base, context path (`/` or `/name`), deployment kind (WAR or exploded) and local JDWP port; save, then inspect the installation. `server.xml` is read-only. The deployment target uses its Host's `appBase` if it is a relative path inside Catalina Base.
4. Produce a WAR using Maven/Gradle. Click **Preview deployment**: verify source, destination, source type and whether an existing target is replaced. **Confirm deployment** only after inspecting the operation. Stop any externally launched Tomcat instances first.
5. Click **Start**, **Debug** or **Stop**. Debug asks Lithe to attach Java Support automatically to the selected port; a missing adapter is a host failure rather than a silently successful debug.
6. To enable opt-in automatic **WAR** redeploy, confirm an initial WAR deployment in the same active plugin session, enable the option **while the server is stopped**, save, and start Tomcat. `server.xml` must have a default Host with `autoDeploy=true`. The source WAR must be rebuilt by the project's build tool; this plugin watches the completed artifact, not source files. On conflict the watcher stops and exposes a message on refresh. Exploded artifacts are deliberately not auto-synchronized while running.

## Safety and limitations

- Project files, Tomcat install files, Core and the plugin package are not modified on plugin startup. The only intended writes are the host-provided per-workspace `tomcat.json` state and deployments inside the **explicitly chosen Catalina Base application directory**.
- All deployment paths are restricted to the local workspace for sources and the Catalina application base for targets. Sources and targets are hashed at preview and immediately before replacement. There are bounded file, byte, ZIP-entry and nesting limits. No subprocess shell execution or network connections are used by the plugin backend. The host owns the process and debugger.
- Deploying a new WAR to a running Tomcat may trigger Tomcat's own re-deployment and reset HTTP sessions. Actual live deployment requires Tomcat `autoDeploy=true`; it is not equivalent to Java debugger Hot Code Replace, and Tomcat's behavior depends on `unpackWARs`, Context and Host settings. See [official Tomcat deployment documentation](https://tomcat.apache.org/tomcat-10.1-doc/deployer-howto.html).
- The plugin does **not** implement: automatic WAR compilation, arbitrary Ant targets, JSP/EL source intelligence, JPA/CDI/JSF IDE model, editing IntelliJ `.iml`/Eclipse `.classpath`, Tomcat Manager credentials/API, remote app servers, fully generalized Context Descriptor deployment or importing shared IDE-global libraries. Those capabilities belong in Java/related plugins and require separate integration work.
- Filesystem replacement across hostile external writers cannot provide a fully transactional guarantee on every OS/filesystem. This version rejects observed symlinks, path escapes and changed digest targets, and keeps an on-disk backup until a replacement completes. Test with a **non-production** Catalina Base.

## Build and test

Requires Go 1.23+, Python 3.10+ and Node.js. Playwright + Chromium is optional for the isolated UI mock. JDK `java`/`javac`/`jar` is needed for the real JVM argv regression.

```sh
go vet ./...
go test -race -count=1 ./...
node --check ui/app.js
python3 scripts/package.py --all
python3 -m unittest discover -s scripts -p 'test_*.py' -v
```

The package generator cross-compiles Linux x64/ARM64, macOS Intel/Apple Silicon and Windows x64, with deterministic ZIP entry timestamps, executable permissions and SHA-256 checks. Runtime behavior has only been exercised in this Linux container; a cross-compiled archive is **not** native platform verification.

### Before merge or release

- Install the archive in the current Lithe preview, verify that both plugin manifests and Java tool resolution are accepted.
- On macOS, Windows and Linux, use a real Tomcat 9/10 instance to start, query `/`, stop, deploy a small servlet WAR and test JDBC/Servlet dependencies. Confirm correct shutdown and no child processes after cancellation.
- Verify Java Debug adapter auto-attach, breakpoints, stack/variables, restart and stop, including an occupied/debug port and a failed Java build.
- Test Web/WTP/IDEA fixtures, WAR vs exploded redeploy, autoDeploy enabled and disabled, spaces/non-ASCII paths, appBase, symlinks and conflict outcomes.
- Check that the plugin package, host installation and selected JDK/Java LS installation files remain unchanged; confirm that no credentials or private project paths are logged.

License: Apache-2.0. Apache and Tomcat are trademarks of the Apache Software Foundation; this project is not endorsed by ASF.

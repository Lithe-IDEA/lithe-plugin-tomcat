# Tomcat 0.2.0 verification — 2026-11-17

## Executed locally (Linux x64 container)

- `go vet ./...` — pass.
- `go test -race -count=1 ./...` — pass; includes initial protocol/RPC/Run and real Java 21 Bootstrap stub; new tests for exploded create, replace, source/target digest conflicts, symlink and `.git` rejection, Eclipse WTP and IntelliJ Web facet read-only detection, parsed server.xml custom appBase / autoDeploy, opt-in live WAR watcher with stable artifact change, watcher rollback on externally changed targets, and one-click Run/Debug adapter alias parameters.
- `node --check ui/app.js` — pass.
- `python3 scripts/package.py --all` — builds 5 Go platform targets and ZIP archives.
- `python3 -m unittest discover -s scripts -p 'test_*.py' -v` — checks ZIP layout/permissions/digests, Linux binary initialize/shutdown, reproducibility, mock Chinese UI project/facet/artifact and preview/confirmation workflow.
- Synthetic Lithe host uses exact `lithe/host/workspace/getContext`, `run/start`, `run/stop` and `run/didExit` request/notification shapes. Java 21 executable regression uses a compiled synthetic `org.apache.catalina.startup.Bootstrap` fixture and localhost JDWP; no real Tomcat components are used.
- Tests use isolated temporary roots, do not mutate the source workspace or installed plugin directory and have no external credentials.

## Not verified / release blockers

1. **Real Tomcat integration** — not run; this container has no Apache Tomcat installation and external archive download was unsuccessful. No claim of HTTP Servlet requests, real app reload or native Tomcat shutdown passing.
2. **Native Lithe host** — macOS and Windows UI, Java Debug adapter attach, file pickers, workbench Run/Stop, and real Java breakpoint handling are pending. The browser test uses a mocked WebBridge.
3. **Cross-platform runtime** — macOS and Windows ZIPs cross-build but were not executed on native systems. Linux ARM64 also not run natively.
4. **JavaEE extras** — JSP/EL completion, auto WAR build, Maven profile wiring, classpath-global libraries and editing Web Facet declarations are separate future work and are not represented as completed here.

Run local checks via README. Review GitHub Actions separately after the PR has been created; no CI result is claimed until observed.

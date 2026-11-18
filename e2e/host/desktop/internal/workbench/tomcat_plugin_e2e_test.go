package workbench

import (
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/1lck/Lithe-IDEA/desktop/internal/core"
)

// These opt-in tests install the Tomcat archive and the released Java Support archive through
// the real installer and drive the workbench as a user does. Java Support declares the java
// tool the launch plan names and the java debug adapter the run's debug type selects. The
// workspace is an Eclipse Dynamic Web Project, which Tomcat serves in place, so no Maven or
// Gradle download is involved. They skip unless the Tomcat archive is named and fail rather
// than skip once a named archive cannot do its work.

const tomcatPluginID = "io.github.lithe-idea.tomcat"

// tomcatInputs are the archives and the Tomcat installation the tests need.
type tomcatInputs struct{ archive, javaArchive, home string }

func requireTomcatInputs(t *testing.T) tomcatInputs {
	t.Helper()
	inputs := tomcatInputs{
		archive:     os.Getenv("LITHE_TEST_TOMCAT_PLUGIN_ARCHIVE"),
		javaArchive: os.Getenv("LITHE_TEST_JAVA_PLUGIN_ARCHIVE"),
		home:        os.Getenv("LITHE_TEST_TOMCAT_HOME"),
	}
	if inputs.archive == "" {
		t.Skip("set LITHE_TEST_TOMCAT_PLUGIN_ARCHIVE to a tomcat-<version>-darwin.zip")
	}
	if inputs.javaArchive == "" {
		t.Fatal("set LITHE_TEST_JAVA_PLUGIN_ARCHIVE to a Lithe-Java-Support-<version>-darwin-<arch>.zip")
	}
	if inputs.home == "" {
		t.Fatal("set LITHE_TEST_TOMCAT_HOME to an unpacked Tomcat (CATALINA_HOME)")
	}
	if _, err := os.Stat(filepath.Join(inputs.home, "bin", "bootstrap.jar")); err != nil {
		t.Fatalf("LITHE_TEST_TOMCAT_HOME is not a Tomcat installation: %v", err)
	}
	return inputs
}

// eclipseWebProject writes an Eclipse Dynamic Web Project below root, served at
// /eclipse/demo, and compiles its one class into the project's output folder, as the Java
// plugin's build does. The page calls the class, so a breakpoint in it stops a request.
func eclipseWebProject(t *testing.T, root string) (source string) {
	t.Helper()
	writePluginFixture(t, root, map[string]string{
		".project": `<?xml version="1.0" encoding="UTF-8"?>
<projectDescription>
  <name>wtp-demo</name>
  <natures>
    <nature>org.eclipse.jdt.core.javanature</nature>
    <nature>org.eclipse.wst.common.modulecore.ModuleCoreNature</nature>
  </natures>
</projectDescription>
`,
		".classpath": `<?xml version="1.0" encoding="UTF-8"?>
<classpath>
	<classpathentry kind="src" path="src"/>
	<classpathentry kind="con" path="org.eclipse.jdt.launching.JRE_CONTAINER"/>
	<classpathentry kind="output" path="build/classes"/>
</classpath>
`,
		".settings/org.eclipse.wst.common.component": `<?xml version="1.0" encoding="UTF-8"?>
<project-modules id="moduleCoreId" project-version="1.5.0">
    <wb-module deploy-name="wtp-demo">
        <wb-resource deploy-path="/" source-path="/WebContent"/>
        <wb-resource deploy-path="/WEB-INF/classes" source-path="/src"/>
        <property name="context-root" value="eclipse/demo"/>
    </wb-module>
</project-modules>
`,
		"WebContent/WEB-INF/web.xml": `<?xml version="1.0" encoding="UTF-8"?>
<web-app xmlns="http://xmlns.jcp.org/xml/ns/javaee" version="3.1"/>
`,
		"WebContent/index.jsp": `<%@ page contentType="text/plain; charset=UTF-8" %><%= demo.wtp.Message.text() %>
`,
		"src/demo/wtp/Message.java": `package demo.wtp;

public final class Message {
    public static String text() {
        String greeting = "Hello from Lithe on Tomcat";
        return greeting;
    }
}
`,
	})
	source = filepath.Join(root, "src", "demo", "wtp", "Message.java")
	classes := filepath.Join(root, "build", "classes")
	if err := os.MkdirAll(classes, 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("javac", "-g", "-source", "8", "-target", "8", "-nowarn", "-d", classes, source).CombinedOutput(); err != nil {
		t.Fatalf("javac: %v\n%s", err, output)
	}
	return source
}

func tomcatFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// openTomcatProject installs both archives, opens a fresh Eclipse Web project, waits for the
// Tomcat model, points the plugin at the Tomcat installation and a free port, and returns the
// one run configuration the model gives.
func openTomcatProject(t *testing.T, h *harness, inputs tomcatInputs) (p *project, installed string, before map[string]string, id, source string, port int) {
	t.Helper()
	installed = installOfficialPluginArchive(t, h, inputs.archive, tomcatPluginID)
	installPluginArchiveInto(t, h.app.plugins, inputs.javaArchive, "io.github.lithe-idea.java-support")
	before = packageDigest(t, installed)
	root := t.TempDir()
	source = eclipseWebProject(t, root)
	h.open(root)
	p = h.app.project
	if p == nil {
		t.Fatal("the project did not open")
	}
	h.app.post(func(a *App) { a.loadTitleRunConfigs(p) })
	instance := awaitBuildModel(t, h, p, "tomcat", 2*time.Minute)
	if got := len(instance.Model.Targets); got != 1 || instance.Model.Targets[0].Name != "wtp-demo" {
		t.Fatalf("the Tomcat model has targets %+v, want only wtp-demo", instance.Model.Targets)
	}
	port = tomcatFreePort(t)
	saveGlobalPluginSettings(t, h, instance, map[string]any{"tomcatHome": inputs.home, "httpPort": port})
	awaitStateWithin(t, h, time.Minute, "the run configurations", func() bool {
		return p.runConfigsSettled() && p.runConfigs.generation > 0
	})
	if p.runConfigs.failure != "" {
		t.Fatalf("the run configurations failed: %s", p.runConfigs.failure)
	}
	var services []core.RunConfiguration
	for _, configuration := range p.runConfigs.list {
		if configuration.Execution == "service" {
			services = append(services, configuration)
		}
	}
	if len(services) != 1 {
		t.Fatalf("the run configurations are %+v, want one Tomcat service", p.runConfigs.list)
	}
	t.Logf("run configuration %q", services[0].Name)
	return p, installed, before, services[0].ID, source, port
}

// getText answers the body of url once it serves 200, or the last error after limit.
func getText(url string, limit time.Duration) (string, error) {
	client := &http.Client{Timeout: limit}
	deadline := time.Now().Add(limit)
	for {
		response, err := client.Get(url)
		if err == nil {
			data, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK {
				return strings.TrimSpace(string(data)), nil
			}
			err = readErr
			if err == nil {
				err = &httpStatusError{status: response.StatusCode}
			}
		}
		if time.Now().After(deadline) {
			return "", err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

type httpStatusError struct{ status int }

func (e *httpStatusError) Error() string { return "HTTP " + strconv.Itoa(e.status) }

func assertPortFree(t *testing.T, port int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("Tomcat's port is still taken after Stop: %v", err)
	}
	_ = listener.Close()
}

// TestTomcatPluginRunsAWebModule runs the Web module from its run configuration: the provider
// writes a private CATALINA_BASE, Lithe starts Tomcat on the project's JDK and shows its log in
// the Run window, the page answers over HTTP, and Stop ends Tomcat and frees its port. The
// installed package and the Tomcat installation stay unchanged.
func TestTomcatPluginRunsAWebModule(t *testing.T) {
	inputs := requireTomcatInputs(t)
	h := newHarness(t, &testKernel{Client: core.NewClient()})
	h.app.logSink = &pluginFilesLog{directory: t.TempDir()}
	homeBefore := packageDigest(t, inputs.home)
	p, installed, before, id, _, port := openTomcatProject(t, h, inputs)

	var op *buildOperation
	var startErr error
	h.app.post(func(a *App) { op, startErr = a.startRunConfiguration(p, id) })
	awaitStateWithin(t, h, 10*time.Second, "the run operation", func() bool { return op != nil || startErr != nil })
	if startErr != nil {
		t.Fatalf("start the Tomcat run: %v", startErr)
	}
	output := func() string { return op.session.output.all() }
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("run summary %q, notice %q, run console:\n%s", op.session.summary, lastNotice(h), output())
		}
	})

	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/eclipse/demo/"
	answers := make(chan string, 1)
	go func() {
		body, err := getText(url, 3*time.Minute)
		if err != nil {
			body = "error: " + err.Error()
		}
		answers <- body
	}()
	var body string
	deadline := time.Now().Add(3*time.Minute + 10*time.Second)
	for body == "" {
		if time.Now().After(deadline) {
			t.Fatalf("Tomcat did not answer; status=%q output:\n%s", p.build.status, output())
		}
		// Pump only: settle waits for every background task, and the running server is one.
		if err := h.driver.Pump(); err != nil {
			t.Fatal(err)
		}
		if !p.build.active() {
			t.Fatalf("the run ended before Tomcat answered; summary=%q output:\n%s", op.session.summary, output())
		}
		select {
		case body = <-answers:
		case <-time.After(100 * time.Millisecond):
		}
	}
	if body != "Hello from Lithe on Tomcat" {
		t.Fatalf("GET %s = %q", url, body)
	}
	awaitStateWithin(t, h, time.Minute, "Tomcat's startup log in the Run window", func() bool {
		return strings.Contains(output(), "Server startup in")
	})

	h.app.post(func(a *App) { a.stopBuildOperation(p) })
	awaitStateWithin(t, h, time.Minute, "the stopped run", func() bool { return !p.build.active() })
	assertPortFree(t, port)

	h.app.closeProject()
	awaitStateWithin(t, h, 30*time.Second, "the closed project", func() bool { return h.app.project == nil })
	h.settle()
	assertPackageUnchanged(t, installed, before)
	assertPackageUnchanged(t, inputs.home, homeBefore)
}

// TestTomcatPluginDebugsAWebModule debugs the same run configuration: Tomcat starts suspended
// on a host-chosen port, the Java debugger attaches, a request stops on a breakpoint in the
// project's source, Continue lets the request finish, and Stop in the Debug window ends Tomcat.
func TestTomcatPluginDebugsAWebModule(t *testing.T) {
	inputs := requireTomcatInputs(t)
	h := newHarness(t, &testKernel{Client: core.NewClient()})
	// Java Support starts its language server, which runs the java adapter, with ${logDir}.
	h.app.logSink = &pluginFilesLog{directory: t.TempDir()}
	p, installed, before, id, source, port := openTomcatProject(t, h, inputs)
	line := tomcatSourceLine(t, source, "return greeting;")
	reference := tomcatBuildReference(t, p, id)
	if facts := p.titleConfigurationFacts(p.buildInstanceIndex(reference), reference); facts.Debuggable == nil || !*facts.Debuggable {
		t.Fatal("the Tomcat run configuration is not debuggable")
	}
	var console func() string
	t.Cleanup(func() {
		if t.Failed() {
			text := ""
			if console != nil {
				text = console()
			}
			t.Logf("build status %q, debug state %q status %q failure %q, notice %q, debug output:\n%s\nrun console:\n%s",
				p.build.status, p.debug.state, p.debug.status, p.debug.failure, lastNotice(h), strings.Join(p.debug.output, ""), text)
		}
	})

	h.app.post(func(a *App) {
		a.toggleBreakpointAt(p, source, line)
		a.chooseTitleConfiguration(p, id)
		a.titleDebug(p)
	})
	awaitStateWithin(t, h, 30*time.Second, "the debugged run", func() bool { return p.build.latest() != nil })
	op := p.build.latest()
	console = func() string { return op.session.output.all() }
	awaitStateWithin(t, h, 5*time.Minute, "the attached debugger", func() bool {
		session := p.debug.activeSession()
		return session != nil && session.adapter.Declaration.ID == "java" && p.debug.state == "running"
	})

	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/eclipse/demo/"
	answers := make(chan string, 1)
	go func() {
		body, err := getText(url, 5*time.Minute)
		if err != nil {
			body = "error: " + err.Error()
		}
		answers <- body
	}()
	awaitStateWithin(t, h, 5*time.Minute, "the stop on the breakpoint", func() bool {
		return p.debug.pausedThread != nil && len(p.debug.frames) > 0
	})
	top := p.debug.frames[0]
	if filepath.Base(top.SourcePath) != "Message.java" || top.Line != line {
		t.Fatalf("stopped at %s:%d in %s, want Message.java:%d", top.SourcePath, top.Line, top.Name, line)
	}
	select {
	case got := <-answers:
		t.Fatalf("the request finished while Tomcat was stopped on the breakpoint: %q", got)
	default:
	}

	h.app.post(func(a *App) { a.continueDebug(p) })
	if got := awaitValueWithin(t, h, time.Minute, answers, "the request after Continue"); got != "Hello from Lithe on Tomcat" {
		t.Fatalf("GET %s after Continue = %q", url, got)
	}

	h.app.post(func(a *App) { a.stopDebugByUser(p) })
	awaitStateWithin(t, h, time.Minute, "the stopped Tomcat", func() bool {
		return !p.build.active() && p.debug.activeSession() == nil
	})
	assertPortFree(t, port)

	h.app.closeProject()
	awaitStateWithin(t, h, 30*time.Second, "the closed project", func() bool { return h.app.project == nil })
	h.settle()
	assertPackageUnchanged(t, installed, before)
}

// tomcatSourceLine is the one-based line of path that contains text.
func tomcatSourceLine(t *testing.T, path, text string) int64 {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for index, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, text) {
			return int64(index + 1)
		}
	}
	t.Fatalf("%s has no line with %q", path, text)
	return 0
}

// tomcatBuildReference is the build provider reference of configuration id.
func tomcatBuildReference(t *testing.T, p *project, id string) core.RunProviderReference {
	t.Helper()
	configuration := p.runConfigs.configuration(id)
	if configuration == nil {
		t.Fatalf("no configuration %q", id)
	}
	reference, ok := configuration.BuildReference()
	if !ok {
		t.Fatalf("configuration %q does not start through a build provider", id)
	}
	return reference
}

package tomcat

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (Config, string, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "Tomcat with spaces")
	workspace := filepath.Join(root, "Workspace")
	for _, dir := range []string{"bin", "lib", "conf", "webapps", "temp"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"bin/bootstrap.jar", "bin/tomcat-juli.jar", "lib/catalina.jar", "conf/server.xml", "conf/catalina.properties"} {
		if err := os.WriteFile(filepath.Join(home, filepath.FromSlash(f)), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(workspace, "target"), 0755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("WEB-INF/web.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("<web-app/>")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "target", "demo.war"), buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	return Config{Home: home, WAR: "target/demo.war", Context: "/demo", DebugPort: 5005}, workspace, home
}
func TestValidationAndJavaLaunch(t *testing.T) {
	c, root, home := fixture(t)
	check, err := Check(c, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(check.TargetPath, filepath.Join("webapps", "demo.war")) {
		t.Fatal(check.TargetPath)
	}
	args := check.Startup(true).Arguments
	if !strings.Contains(strings.Join(args, " "), "127.0.0.1:5005") {
		t.Fatalf("no localhost JDWP: %v", args)
	}
	if !strings.Contains(strings.Join(args, " "), "Tomcat with spaces") {
		t.Fatalf("argument split/space lost: %v", args)
	}
	if check.Startup(false).WorkingDirectory != "." {
		t.Fatal("wrong workspace working directory")
	}
	for _, bad := range []string{"../outside.war", "/etc/passwd", "../../evil.war", "target/not-a-war.exe", "target/../../demo.war"} {
		c.WAR = bad
		if _, err = Check(c, root, true); err == nil {
			t.Fatalf("accepted unsafe path: %s", bad)
		}
	}
	c.WAR = "target/demo.war"
	c.Context = "/a/b"
	if _, err = Check(c, root, false); err == nil {
		t.Fatal("accepted nested context path")
	}
	c.Context = "/../x"
	if _, err = Check(c, root, false); err == nil {
		t.Fatal("accepted traversal context")
	}
	c.Context = "/demo"
	c.DebugPort = 80
	if _, err = Check(c, root, false); err == nil {
		t.Fatal("accepted privileged debug port")
	}
	// Starting Tomcat must not require an already-packaged WAR.
	c.DebugPort = 5005
	c.WAR = "target/unbuilt.war"
	if _, err := Check(c, root, false); err != nil {
		t.Fatalf("blocked startup before build: %v", err)
	}
	if _, err := os.Lstat(home); err != nil {
		t.Fatal(err)
	}
}
func TestDeployPreviewIntegrityAndOverwrite(t *testing.T) {
	c, root, home := fixture(t)
	checked, err := Check(c, root, true)
	if err != nil {
		t.Fatal(err)
	}
	sourceBefore, err := os.ReadFile(checked.WARPath)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := InspectDeploy(checked)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(preview.Target); !os.IsNotExist(err) {
		t.Fatal("preview wrote file")
	}
	if _, err := Deploy(checked, "no-token"); err == nil {
		t.Fatal("deployed without confirmation")
	}
	if _, err := Deploy(checked, preview.Token); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(preview.Target)
	if err != nil || !bytes.Equal(got, sourceBefore) {
		t.Fatal("WAR changed during deploy", err)
	}
	if _, err := Deploy(checked, preview.Token); err == nil {
		t.Fatal("replayed stale preview")
	}
	replace, err := InspectDeploy(checked)
	if err != nil {
		t.Fatal(err)
	}
	if replace.Action != "replace" {
		t.Fatal(replace.Action)
	}
	if _, err := Deploy(checked, replace.Token); err != nil {
		t.Fatal(err)
	}
	// External mutation after preview must invalidate consent.
	stale, err := InspectDeploy(checked)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale.Target, []byte("changed by external process"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Deploy(checked, stale.Token); err == nil {
		t.Fatal("overwrote external changes")
	}
	if err := os.Remove(stale.Target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "conf", "server.xml"), stale.Target); err == nil {
		if _, err := InspectDeploy(checked); err == nil {
			t.Fatal("accepted linked target")
		}
		if err := os.Remove(stale.Target); err != nil {
			t.Fatal(err)
		}
	}
	srcAfter, err := os.ReadFile(checked.WARPath)
	if err != nil || !bytes.Equal(sourceBefore, srcAfter) {
		t.Fatal("source modified")
	}
	entries, err := os.ReadDir(checked.Webapps)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("left deployment staging or backup files: %v", entries)
	}
}
func TestRejectSymlinkAndCorruptWAR(t *testing.T) {
	c, root, home := fixture(t)
	outside := filepath.Join(t.TempDir(), "elsewhere.war")
	data, err := os.ReadFile(filepath.Join(root, c.WAR))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "target", "linked.war")); err == nil {
		c.WAR = "target/linked.war"
		if _, err := Check(c, root, true); err == nil {
			t.Fatal("accepted symlinked source")
		}
	}
	c.WAR = "target/demo.war"
	if err := os.WriteFile(filepath.Join(root, c.WAR), []byte("not actually a WAR"), 0600); err != nil {
		t.Fatal(err)
	}
	check, err := Check(c, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InspectDeploy(check); err == nil {
		t.Fatal("accepted non-zip WAR")
	}
	if err := os.RemoveAll(filepath.Join(home, "webapps")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(home, "webapps")); err == nil {
		if _, err := Check(c, root, false); err == nil {
			t.Fatal("accepted redirected webapps")
		}
	}
}
func TestWorkspaceStateOnly(t *testing.T) {
	state := filepath.Join(t.TempDir(), "private-state")
	got, err := LoadConfig(state)
	if err != nil || got.DebugPort != 5005 {
		t.Fatal(got, err)
	}
	c := Config{Home: filepath.Join(t.TempDir(), "not inspected yet"), Context: "/test", WAR: "target/out.war", DebugPort: 5656}
	if err := SaveConfig(state, c); err != nil {
		t.Fatal(err)
	}
	saved, err := LoadConfig(state)
	if err != nil || saved.Context != "/test" {
		t.Fatal(saved, err)
	}
	f, err := os.Open(filepath.Join(state, "tomcat.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size() > 16<<10 {
		t.Fatal("state too large")
	}
	if _, err := io.ReadAll(f); err != nil {
		t.Fatal(err)
	}
}

func TestRejectArchiveTraversalAndOversizedContents(t *testing.T) {
	c, root, _ := fixture(t)
	path := filepath.Join(root, c.WAR)
	for _, name := range []string{"../escape.txt", "x/../../escape", "/absolute.txt", "C:/windows.txt", "dir\\tricky"} {
		var data bytes.Buffer
		w := zip.NewWriter(&data)
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte("invalid"))
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data.Bytes(), 0644); err != nil {
			t.Fatal(err)
		}
		checked, err := Check(c, root, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := InspectDeploy(checked); err == nil {
			t.Fatalf("accepted unsafe ZIP name %q", name)
		}
	}
}

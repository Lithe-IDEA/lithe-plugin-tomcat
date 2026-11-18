package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lithe-IDEA/lithe-plugin-tomcat/internal/tomcat"
)

func modifyWAR(t *testing.T, p, content string) {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	f, err := writer.Create("WEB-INF/web.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("<web-app>" + content + "</web-app>")); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
}
func TestLiveWARWatchRequiresApprovalAndStopsOnExternalMutation(t *testing.T) {
	c, root, home := fakeTomcat(t)
	c.WatchChanges = true
	c.Artifact = "war"
	if err := os.WriteFile(filepath.Join(home, "conf", "server.xml"), []byte(`<Server><Service><Engine><Host name="localhost" appBase="webapps" autoDeploy="true"/></Engine></Service></Server>`), 0644); err != nil {
		t.Fatal(err)
	}
	s := newSession()
	key := "ctx:1"
	s.reserve(key)
	s.finishStart(key, "r-1")
	if got := s.startWatch(key, "r-1", c, root); !strings.Contains(got, "Confirm") {
		t.Fatalf("expected approval required, got %q", got)
	}
	checked, err := tomcat.Check(c, root, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tomcat.InspectDeploy(checked)
	if err != nil {
		t.Fatal(err)
	}
	deployed, err := tomcat.Deploy(checked, p.Token)
	if err != nil {
		t.Fatal(err)
	}
	s.approval(key, deployed)
	before := watchPollInterval
	watchPollInterval = 120 * time.Millisecond
	defer func() { s.cancelWatch(key); time.Sleep(100 * time.Millisecond); watchPollInterval = before }()
	if got := s.startWatch(key, "r-1", c, root); got != "" {
		t.Fatal(got)
	}
	modifyWAR(t, checked.WARPath, "new")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, e := os.ReadFile(deployed.Target)
		source, _ := os.ReadFile(checked.WARPath)
		if e == nil && bytes.Equal(data, source) {
			break
		}
		time.Sleep(45 * time.Millisecond)
	}
	data, err := os.ReadFile(deployed.Target)
	source, _ := os.ReadFile(checked.WARPath)
	if err != nil || !bytes.Equal(data, source) {
		t.Fatalf("automatic update did not deploy WAR: %s (%v)", data, err)
	}
	// A third party changing the deployed file is not overwritten.
	if err := os.WriteFile(deployed.Target, []byte("external changes"), 0644); err != nil {
		t.Fatal(err)
	}
	modifyWAR(t, checked.WARPath, "newer")
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		watching, reason := s.watchStatus(key)
		if !watching && strings.Contains(reason, "externally") {
			break
		}
		time.Sleep(45 * time.Millisecond)
	}
	_, reason := s.watchStatus(key)
	if !strings.Contains(reason, "externally") {
		t.Fatalf("watcher didn't detect conflict: %q", reason)
	}
	content, err := os.ReadFile(deployed.Target)
	if err != nil || string(content) != "external changes" {
		t.Fatalf("external changes lost: %q (%v)", content, err)
	}
	s.onRunExit("r-1")
	if s.runID(key) != "" {
		t.Fatal("run not cleared")
	}
}
func TestLiveWARIsRejectedWhenTomcatAutoDeployDisabled(t *testing.T) {
	c, root, home := fakeTomcat(t)
	c.WatchChanges = true
	c.Artifact = "war"
	if err := os.WriteFile(filepath.Join(home, "conf", "server.xml"), []byte(`<Server><Service><Engine><Host name="localhost" autoDeploy="false"/></Engine></Service></Server>`), 0644); err != nil {
		t.Fatal(err)
	}
	checked, err := tomcat.Check(c, root, true)
	if err != nil {
		t.Fatal(err)
	}
	s := newSession()
	s.reserve("ctx:1")
	s.finishStart("ctx:1", "r-1")
	if s.canDeploy("ctx:1", c, checked) {
		t.Fatal("enabled live deploy when Tomcat explicitly disables it")
	}
	if got := s.startWatch("ctx:1", "r-1", c, root); !strings.Contains(got, "autoDeploy") {
		t.Fatalf("missing diagnosed prerequisite: %s", got)
	}
}

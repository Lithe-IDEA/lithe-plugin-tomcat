package tomcat

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplodedDeploymentAndConflicts(t *testing.T) {
	cfg, root, home := fixture(t)
	cfg.Artifact = "exploded"
	cfg.Exploded = "target/app"
	web := filepath.Join(root, "target", "app")
	for _, dir := range []string{"WEB-INF/classes/demo", "assets"} {
		if err := os.MkdirAll(filepath.Join(web, filepath.FromSlash(dir)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(web, filepath.FromSlash(name)), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("WEB-INF/web.xml", "<web-app/>")
	write("WEB-INF/classes/demo/App.class", "fake-class")
	write("assets/style.css", "body{margin:0}")
	checked, err := Check(cfg, root, true)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := InspectDeploy(checked)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Artifact != "exploded" || plan.Files != 3 || plan.Action != "create" {
		t.Fatalf("plan=%+v", plan)
	}
	if _, err := os.Lstat(plan.Target); !os.IsNotExist(err) {
		t.Fatal("preview modified destination")
	}
	if _, err := Deploy(checked, "invalid"); err == nil {
		t.Fatal("deployed without confirmation")
	}
	if _, err := Deploy(checked, plan.Token); err != nil {
		t.Fatal(err)
	}
	deployed, err := os.ReadFile(filepath.Join(plan.Target, "WEB-INF/classes/demo/App.class"))
	if err != nil || string(deployed) != "fake-class" {
		t.Fatal(err, string(deployed))
	}
	if _, err := Deploy(checked, plan.Token); err == nil {
		t.Fatal("reused stale preview token")
	}
	replacement, err := InspectDeploy(checked)
	if err != nil {
		t.Fatal(err)
	}
	write("assets/style.css", "body{background:black}")
	if _, err := Deploy(checked, replacement.Token); err == nil {
		t.Fatal("accepted source mutation")
	}
	replacement, err = InspectDeploy(checked)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Action != "replace" {
		t.Fatal(replacement)
	}
	if _, err := Deploy(checked, replacement.Token); err != nil {
		t.Fatal(err)
	}
	newFile, err := os.ReadFile(filepath.Join(plan.Target, "assets/style.css"))
	if err != nil || !bytes.Equal(newFile, []byte("body{background:black}")) {
		t.Fatal(err)
	}
	stale, err := InspectDeploy(checked)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plan.Target, "assets/style.css"), []byte("external"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Deploy(checked, stale.Token); err == nil {
		t.Fatal("overwrote externally changed deployed directory")
	}
	// Never replace an exploded deployment while a WAR of the same context exists.
	if err := os.WriteFile(filepath.Join(home, "webapps", "demo.war"), []byte("conflict"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectDeploy(checked); err == nil {
		t.Fatal("accepted conflicting WAR and exploded deployment")
	}
}

func TestExplodedSymlinkAndMetadataRejection(t *testing.T) {
	cfg, root, _ := fixture(t)
	cfg.Artifact = "exploded"
	cfg.Exploded = "target/app"
	web := filepath.Join(root, "target", "app")
	if err := os.MkdirAll(filepath.Join(web, "WEB-INF"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "WEB-INF", "web.xml"), []byte("<web-app/>"), 0644); err != nil {
		t.Fatal(err)
	}
	checked, err := Check(cfg, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "target", "demo.war"), filepath.Join(web, "linked.war")); err == nil {
		if _, err := InspectDeploy(checked); err == nil {
			t.Fatal("accepted source symlink")
		}
		if err := os.Remove(filepath.Join(web, "linked.war")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(web, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectDeploy(checked); err == nil {
		t.Fatal("accepted .git metadata in deployment")
	}
	if err := os.Remove(filepath.Join(web, ".git")); err != nil {
		t.Fatal(err)
	}
	cfg.Exploded = "../escape"
	if _, err := Check(cfg, root, true); err == nil {
		t.Fatal("accepted exploded path traversal")
	}
	cfg.Exploded = "target/app"
	cfg.WatchChanges = true
	if _, err := ValidateConfig(cfg); err == nil {
		t.Fatal("accepted exploded auto redeployment")
	}
}

func TestDetectWebFacetsAndArtifacts(t *testing.T) {
	_, root, _ := fixture(t)
	eclipse := filepath.Join(root, "old-web")
	idea := filepath.Join(root, "idea-web")
	for _, dir := range []string{
		"old-web/WebContent/WEB-INF", "old-web/.settings", "old-web/target",
		"idea-web/custom/web/WEB-INF", "idea-web/target/myweb/WEB-INF",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, contents := range map[string]string{
		"old-web/.project": "<projectDescription><name>old-web</name></projectDescription>",
		"old-web/.settings/org.eclipse.wst.common.project.facet.core.xml": "<faceted-project><installed facet=\"jst.web\" version=\"3.1\"/></faceted-project>",
		"old-web/.settings/org.eclipse.wst.common.component":              "<project-modules><wb-module><wb-resource deploy-path=\"/\" source-path=\"/WebContent\"/></wb-module></project-modules>",
		"old-web/WebContent/WEB-INF/web.xml":                              "<web-app/>",
		"idea-web/idea-web.iml":                                           "<module><component name=\"FacetManager\"><facet type=\"web\"><configuration><webroots><root url=\"file://$MODULE_DIR$/custom/web\"/></webroots><descriptors><deploymentDescriptor url=\"file://$MODULE_DIR$/custom/web/WEB-INF/web.xml\"/></descriptors></configuration></facet></component></module>",
		"idea-web/custom/web/WEB-INF/web.xml":                             "<web-app/>",
		"idea-web/target/myweb/WEB-INF/web.xml":                           "<web-app/>",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(contents), 0644); err != nil {
			t.Fatal(err)
		}
	}
	_ = eclipse
	_ = idea
	if err := os.WriteFile(filepath.Join(root, "old-web/target/old-web.war"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}
	found, err := DiscoverWebModules(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 3 {
		t.Fatalf("found %d: %+v", len(found), found)
	}
	byName := map[string]WebModule{}
	for _, m := range found {
		byName[m.Name] = m
	}
	if !strings.Contains(byName["old-web"].Facet, "Eclipse") || byName["old-web"].WebRoot != "old-web/WebContent" || len(byName["old-web"].WARCandidates) != 1 {
		t.Fatalf("eclipse: %+v", byName["old-web"])
	}
	if !strings.Contains(byName["idea-web"].Facet, "IntelliJ") || byName["idea-web"].WebRoot != "idea-web/custom/web" || len(byName["idea-web"].ExplodedCandidates) != 1 {
		t.Fatalf("idea: %+v", byName["idea-web"])
	}
}

func TestServerAutoDeployAndCustomAppBase(t *testing.T) {
	cfg, root, home := fixture(t)
	xml := `<Server><Service><Connector port="8090" protocol="HTTP/1.1"/><Connector port="8009" protocol="AJP/1.3"/><Engine><Host name="localhost" appBase="apps" autoDeploy="false" deployOnStartup="true"/></Engine></Service></Server>`
	if err := os.WriteFile(filepath.Join(home, "conf/server.xml"), []byte(xml), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "apps"), 0755); err != nil {
		t.Fatal(err)
	}
	checked, err := Check(cfg, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if checked.Server.AutoDeploy || !checked.Server.XMLValid || checked.Server.HTTPPorts[0] != 8090 || !strings.HasSuffix(checked.TargetPath, filepath.Join("apps", "demo.war")) {
		t.Fatalf("server=%+v target=%s", checked.Server, checked.TargetPath)
	}
	xml = strings.ReplaceAll(xml, "appBase=\"apps\"", "appBase=\"../../other\"")
	if err := os.WriteFile(filepath.Join(home, "conf/server.xml"), []byte(xml), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(cfg, root, false); err == nil {
		t.Fatal("accepted escaping appBase")
	}
}

func TestDiscoverNestedWebModulesWithoutWalkingBuildDirectories(t *testing.T) {
	_, root, _ := fixture(t)
	for _, entry := range []string{
		"apps/services/booking/src/main/webapp/WEB-INF",
		"apps/services/booking/target",
		"apps/libs/site/build/libs",
		"node_modules/fake/src/main/webapp/WEB-INF",
		"target/generated/src/main/webapp/WEB-INF",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(entry)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{
		"apps/services/booking/pom.xml": "<project/>",
		"apps/services/booking/src/main/webapp/WEB-INF/web.xml": "<web-app/>",
		"apps/services/booking/target/booking.war": "placeholder",
		"apps/libs/site/build.gradle": "plugins { id 'war' }",
		"apps/libs/site/build/libs/site.war": "placeholder",
		"node_modules/fake/src/main/webapp/WEB-INF/web.xml": "<web-app/>",
		"target/generated/src/main/webapp/WEB-INF/web.xml": "<web-app/>",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	found, err := DiscoverWebModules(root)
	if err != nil {
		t.Fatal(err)
	}
	byPath := make(map[string]WebModule)
	for _, item := range found {
		if strings.HasPrefix(item.Path, "node_modules/") || strings.HasPrefix(item.Path, "target/") {
			t.Fatalf("scanned excluded build/dependency directory: %+v", item)
		}
		byPath[item.Path] = item
	}
	booking, ok := byPath["apps/services/booking"]
	if !ok || booking.Model != "maven" || booking.WebRoot != "apps/services/booking/src/main/webapp" ||
		len(booking.WARCandidates) != 1 || booking.WARCandidates[0] != "apps/services/booking/target/booking.war" {
		t.Fatalf("nested Maven web module: %+v (present=%v)", booking, ok)
	}
	site, ok := byPath["apps/libs/site"]
	if !ok || site.Model != "gradle" || len(site.WARCandidates) != 1 ||
		site.WARCandidates[0] != "apps/libs/site/build/libs/site.war" {
		t.Fatalf("nested Gradle WAR module: %+v (present=%v)", site, ok)
	}
}

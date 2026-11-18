// Package tomcat implements local Tomcat run and deploy planning. It never
// starts a process or mutates the source project as a side effect of planning.
package tomcat

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const MaxWARSize int64 = 512 << 20

var contextName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

// Config is stored only under the host's per-workspace plugin state directory.
// War is workspace-relative; host-provided workspace roots are authoritative.
type Config struct {
	Home         string `json:"home"`
	Base         string `json:"base"`
	WAR          string `json:"war"`
	Exploded     string `json:"exploded"`
	Artifact     string `json:"artifact"`
	Context      string `json:"context"`
	DebugPort    int    `json:"debugPort"`
	WatchChanges bool   `json:"watchChanges"`
}

type Checked struct {
	Config
	HomePath     string
	BasePath     string
	Webapps      string
	Server       ServerInfo
	WARPath      string
	ExplodedPath string
	TargetPath   string
}

func ValidateConfig(c Config) (Config, error) {
	c.Home = strings.TrimSpace(c.Home)
	c.Base = strings.TrimSpace(c.Base)
	c.WAR = strings.TrimSpace(c.WAR)
	c.Exploded = strings.TrimSpace(c.Exploded)
	c.Artifact = strings.TrimSpace(c.Artifact)
	c.Context = strings.TrimSpace(c.Context)
	if c.Artifact == "" {
		c.Artifact = "war"
	}
	if c.Artifact != "war" && c.Artifact != "exploded" {
		return c, errors.New("artifact must be war or exploded")
	}
	if c.WatchChanges && c.Artifact != "war" {
		return c, errors.New("automatic redeploy currently supports WAR artifacts only")
	}
	if c.Home == "" || !filepath.IsAbs(c.Home) {
		return c, errors.New("Tomcat home must be an absolute directory")
	}
	if c.Base != "" && !filepath.IsAbs(c.Base) {
		return c, errors.New("Catalina base must be an absolute directory or empty")
	}
	if c.Context == "" {
		c.Context = "/"
	}
	if c.Context != "/" && (!strings.HasPrefix(c.Context, "/") || !contextName.MatchString(c.Context[1:]) || strings.EqualFold(c.Context, "/ROOT")) {
		return c, errors.New("context must be / or a single /name segment (letters, digits, _ or -)")
	}
	if c.DebugPort == 0 {
		c.DebugPort = 5005
	}
	if c.DebugPort < 1024 || c.DebugPort > 65535 {
		return c, errors.New("debug port must be 1024–65535")
	}
	for _, p := range []struct{ name, value string }{{"WAR", c.WAR}, {"exploded folder", c.Exploded}} {
		if p.value == "" {
			continue
		}
		clean := filepath.Clean(p.value)
		if filepath.IsAbs(p.value) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || (runtime.GOOS != "windows" && strings.Contains(p.value, "\\")) || strings.Contains(p.value, ":") {
			return c, fmt.Errorf("%s must be a workspace-relative path", p.name)
		}
	}
	if c.WAR != "" && !strings.EqualFold(filepath.Ext(c.WAR), ".war") {
		return c, errors.New("WAR must have a .war extension")
	}
	return c, nil
}

func realDirectory(name, p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("%s path is empty", name)
	}
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	r, err = filepath.Abs(r)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(r)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("%s is not a directory", name)
	}
	return r, nil
}

func regularFile(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("not a regular file (symlinks are not allowed)")
	}
	return nil
}

func Check(c Config, workspaceRoot string, requireWAR bool) (*Checked, error) {
	c, err := ValidateConfig(c)
	if err != nil {
		return nil, err
	}
	home, err := realDirectory("Tomcat home", c.Home)
	if err != nil {
		return nil, err
	}
	base := home
	if c.Base != "" {
		base, err = realDirectory("Catalina base", c.Base)
		if err != nil {
			return nil, err
		}
	}
	for _, item := range []string{"bin/bootstrap.jar", "bin/tomcat-juli.jar", "lib/catalina.jar"} {
		p := filepath.Join(home, filepath.FromSlash(item))
		if err := regularFile(p); err != nil {
			return nil, fmt.Errorf("Tomcat installation missing %s: %w", item, err)
		}
	}
	for _, item := range []string{"conf/server.xml", "conf/catalina.properties"} {
		if err := regularFile(filepath.Join(base, filepath.FromSlash(item))); err != nil {
			return nil, fmt.Errorf("Catalina base missing %s: %w", item, err)
		}
	}
	server := inspectServerXML(base)
	webapps, err := checkedAppBase(base, server)
	if err != nil {
		return nil, err
	}
	// Explicitly refuse a redirected webapps folder; it is the mutation boundary.
	if st, err := os.Lstat(webapps); err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("Catalina base webapps must be a real directory, not a symlink")
	}
	c.Home, c.Base = home, base
	checked := &Checked{Config: c, HomePath: home, BasePath: base, Webapps: webapps, Server: server}
	name := "ROOT"
	if c.Context != "/" {
		name = c.Context[1:]
	}
	if c.Artifact == "exploded" {
		checked.TargetPath = filepath.Join(webapps, name)
	} else {
		checked.TargetPath = filepath.Join(webapps, name+".war")
	}
	if !requireWAR {
		return checked, nil
	}
	chosen := c.WAR
	if c.Artifact == "exploded" {
		chosen = c.Exploded
	}
	if chosen == "" {
		return nil, fmt.Errorf("select a workspace-relative %s before deploying", c.Artifact)
	}
	root, err := realDirectory("Workspace", workspaceRoot)
	if err != nil {
		return nil, err
	}
	// Require the declared path to stay inside this workspace, even through symlinks.
	candidate := filepath.Join(root, chosen)
	st, err := os.Lstat(candidate)
	if err != nil || st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("artifact must exist and must not be a symlink")
	}
	if c.Artifact == "war" && !st.Mode().IsRegular() {
		return nil, errors.New("WAR must be a regular file")
	}
	if c.Artifact == "exploded" && !st.IsDir() {
		return nil, errors.New("exploded artifact must be a directory")
	}
	real, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, errors.New("artifact escapes the workspace")
	}
	st, err = os.Stat(real)
	if err != nil {
		return nil, err
	}
	if c.Artifact == "exploded" {
		checked.ExplodedPath = real
		return checked, nil
	}
	if !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > MaxWARSize {
		return nil, errors.New("WAR must be a nonempty regular file no larger than 512 MiB")
	}
	checked.WARPath = real
	return checked, nil
}

type RunPlan struct {
	Executable       map[string]string `json:"executable"`
	Arguments        []string          `json:"arguments"`
	WorkingDirectory string            `json:"workingDirectory"`
	Environment      map[string]string `json:"environment"`
}

// Startup runs Tomcat Bootstrap in the JVM selected by Lithe's Java tool.
// No shell, catalina.sh, cmd.exe, or user-supplied JVM argument is interpolated.
func (c *Checked) Startup(debug bool) RunPlan {
	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
	}
	classpath := filepath.Join(c.HomePath, "bin", "bootstrap.jar") + sep + filepath.Join(c.HomePath, "bin", "tomcat-juli.jar")
	args := []string{
		"-Dcatalina.home=" + c.HomePath,
		"-Dcatalina.base=" + c.BasePath,
		"-Djava.io.tmpdir=" + filepath.Join(c.BasePath, "temp"),
		"-Djava.util.logging.config.file=" + filepath.Join(c.BasePath, "conf", "logging.properties"),
		"-Djava.util.logging.manager=org.apache.juli.ClassLoaderLogManager",
	}
	if debug {
		args = append(args, "-agentlib:jdwp=transport=dt_socket,server=y,suspend=n,address=127.0.0.1:"+strconv.Itoa(c.DebugPort))
	}
	args = append(args, "-classpath", classpath, "org.apache.catalina.startup.Bootstrap", "start")
	return RunPlan{Executable: map[string]string{"kind": "tool", "tool": "java"}, Arguments: args, WorkingDirectory: ".", Environment: map[string]string{"CATALINA_HOME": c.HomePath, "CATALINA_BASE": c.BasePath}}
}

func HashToken(parts ...string) string {
	h := sha256.New()
	for _, s := range parts {
		_, _ = h.Write([]byte(strconv.Itoa(len(s)) + ":" + s))
	}
	return hex.EncodeToString(h.Sum(nil))
}

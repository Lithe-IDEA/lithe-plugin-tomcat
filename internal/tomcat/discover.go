package tomcat

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WebModule is a bounded, read-only projection of a Java Web Facet and the
// build artifact candidates. The Java plugin remains the authority for JDKs,
// project libraries, classpaths and Maven/Gradle imports.
type WebModule struct {
	Name               string   `json:"name"`
	Path               string   `json:"path"`
	Model              string   `json:"model"`
	Facet              string   `json:"facet"`
	WebRoot            string   `json:"webRoot"`
	Descriptor         string   `json:"descriptor,omitempty"`
	WARCandidates      []string `json:"warCandidates"`
	ExplodedCandidates []string `json:"explodedCandidates"`
}

func smallXML(path string, value any) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	decoder := xml.NewDecoder(io.LimitReader(f, 1<<20))
	return decoder.Decode(value) == nil
}

func existsDir(path string) bool {
	s, e := os.Lstat(path)
	return e == nil && s.IsDir() && s.Mode()&os.ModeSymlink == 0
}
func existsFile(path string) bool {
	s, e := os.Lstat(path)
	return e == nil && s.Mode().IsRegular()
}
func workspaceRelative(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// DiscoverWebModules uses a bounded breadth-first walk (at most three folder
// levels below the workspace). A Maven/Gradle aggregator may nest Web modules
// deeper than one level; deployment sources still must pass Check's stronger
// workspace/real-path validation before they can be used.
func DiscoverWebModules(root string) ([]WebModule, error) {
	root, err := realDirectory("Workspace", root)
	if err != nil {
		return nil, err
	}
	const maxDirectories = 256
	const maxDepth = 3
	const maxEntries = 20000
	candidates := []string{root}
	depths := []int{0}
	remaining := maxEntries
	for index := 0; index < len(candidates) && len(candidates) < maxDirectories; index++ {
		if depths[index] >= maxDepth {
			continue
		}
		children, err := os.ReadDir(candidates[index])
		if err != nil {
			if index == 0 {
				return nil, err
			}
			continue
		}
		if len(children) > remaining {
			if index == 0 {
				return nil, os.ErrInvalid
			}
			break // Return candidates seen so far; never scan unbounded trees.
		}
		remaining -= len(children)
		for _, child := range children {
			if len(candidates) >= maxDirectories {
				break
			}
			if !child.IsDir() || strings.HasPrefix(child.Name(), ".") {
				continue
			}
			// Build outputs, dependencies and source/resource trees cannot
			// contain an independent workspace module at this scope.
			switch child.Name() {
			case "target", "build", "node_modules", "vendor", "out", "bin", "dist", "src":
				continue
			}
			nested := filepath.Join(candidates[index], child.Name())
			if existsDir(nested) {
				candidates = append(candidates, nested)
				depths = append(depths, depths[index]+1)
			}
		}
	}

	modules := make([]WebModule, 0)
	for _, dir := range candidates {
		name := filepath.Base(dir)
		m := WebModule{Name: name, Path: workspaceRelative(root, dir), Model: "legacy", WARCandidates: []string{}, ExplodedCandidates: []string{}}
		if existsFile(filepath.Join(dir, "pom.xml")) {
			m.Model = "maven"
		} else if existsFile(filepath.Join(dir, "build.gradle")) || existsFile(filepath.Join(dir, "build.gradle.kts")) {
			m.Model = "gradle"
		} else if existsFile(filepath.Join(dir, ".project")) {
			m.Model = "eclipse"
		}
		roots := []string{"src/main/webapp", "WebContent", "WebRoot", "webapp", "src/webapp"}
		for _, rp := range roots {
			full := filepath.Join(dir, filepath.FromSlash(rp))
			if existsDir(full) {
				m.WebRoot = workspaceRelative(root, full)
				descriptor := filepath.Join(full, "WEB-INF", "web.xml")
				if existsFile(descriptor) {
					m.Descriptor = workspaceRelative(root, descriptor)
				}
				break
			}
		}
		var facet struct {
			XMLName   xml.Name `xml:"faceted-project"`
			Installed []struct {
				Facet   string `xml:"facet,attr"`
				Version string `xml:"version,attr"`
			} `xml:"installed"`
		}
		if smallXML(filepath.Join(dir, ".settings", "org.eclipse.wst.common.project.facet.core.xml"), &facet) {
			for _, item := range facet.Installed {
				if item.Facet == "jst.web" {
					m.Facet = "Eclipse WTP Web " + item.Version
					break
				}
			}
		}
		var comp struct {
			Modules []struct {
				Resources []struct {
					Path   string `xml:"source-path,attr"`
					Deploy string `xml:"deploy-path,attr"`
				} `xml:"wb-resource"`
			} `xml:"wb-module"`
		}
		if smallXML(filepath.Join(dir, ".settings", "org.eclipse.wst.common.component"), &comp) {
			if m.Facet == "" {
				m.Facet = "Eclipse WTP"
			}
			if m.WebRoot == "" {
				for _, mod := range comp.Modules {
					for _, r := range mod.Resources {
						if r.Deploy != "/" {
							continue
						}
						relative := strings.TrimPrefix(r.Path, "/")
						candidate := filepath.Join(dir, filepath.FromSlash(relative))
						if existsDir(candidate) && workspaceRelative(root, candidate) != "" {
							m.WebRoot = workspaceRelative(root, candidate)
							break
						}
					}
				}
			}
		}
		// IDEA Web facets are recorded in module .iml files. Read known
		// local .iml names only; never expand IDE global path macros.
		imls := []string{filepath.Join(dir, name+".iml")}
		if entry, err := os.ReadDir(dir); err == nil && len(entry) < 5000 {
			for _, e := range entry {
				if len(imls) >= 17 {
					break
				}
				if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".iml") {
					p := filepath.Join(dir, e.Name())
					if p != imls[0] {
						imls = append(imls, p)
					}
				}
			}
		}
		for _, iml := range imls {
			var module struct {
				Components []struct {
					Facets []struct {
						Type          string `xml:"type,attr"`
						Configuration struct {
							WebRoots []struct {
								URL string `xml:"url,attr"`
							} `xml:"webroots>root"`
							Descriptors []struct {
								URL string `xml:"url,attr"`
							} `xml:"descriptors>deploymentDescriptor"`
						} `xml:"configuration"`
					} `xml:"facet"`
				} `xml:"component"`
			}
			if !smallXML(iml, &module) {
				continue
			}
			for _, comp := range module.Components {
				for _, f := range comp.Facets {
					if f.Type != "web" {
						continue
					}
					m.Facet = "IntelliJ Web"
					if m.Model == "legacy" {
						m.Model = "idea"
					}
					for _, rootURL := range f.Configuration.WebRoots {
						const prefix = "file://$MODULE_DIR$/"
						if !strings.HasPrefix(rootURL.URL, prefix) {
							continue
						}
						p := filepath.Join(filepath.Dir(iml), filepath.FromSlash(strings.TrimPrefix(rootURL.URL, prefix)))
						if existsDir(p) {
							if rel := workspaceRelative(root, p); rel != "" {
								m.WebRoot = rel
								break
							}
						}
					}
					for _, d := range f.Configuration.Descriptors {
						const prefix = "file://$MODULE_DIR$/"
						if !strings.HasPrefix(d.URL, prefix) {
							continue
						}
						p := filepath.Join(filepath.Dir(iml), filepath.FromSlash(strings.TrimPrefix(d.URL, prefix)))
						if existsFile(p) {
							m.Descriptor = workspaceRelative(root, p)
							break
						}
					}
				}
			}
		}
		for _, artifactDir := range []string{"target", "build/libs", "build/distributions"} {
			base := filepath.Join(dir, filepath.FromSlash(artifactDir))
			entries, err := os.ReadDir(base)
			if err != nil || len(entries) > 3000 {
				continue
			}
			for _, e := range entries {
				if len(m.WARCandidates) >= 16 && len(m.ExplodedCandidates) >= 16 {
					break
				}
				p := filepath.Join(base, e.Name())
				if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".war") && existsFile(p) && len(m.WARCandidates) < 16 {
					m.WARCandidates = append(m.WARCandidates, workspaceRelative(root, p))
				}
				if e.IsDir() && len(m.ExplodedCandidates) < 16 && existsDir(p) && existsDir(filepath.Join(p, "WEB-INF")) {
					m.ExplodedCandidates = append(m.ExplodedCandidates, workspaceRelative(root, p))
				}
			}
		}
		if m.WebRoot == "" && m.Facet == "" && len(m.WARCandidates) == 0 && len(m.ExplodedCandidates) == 0 {
			continue
		}
		modules = append(modules, m)
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	return modules, nil
}

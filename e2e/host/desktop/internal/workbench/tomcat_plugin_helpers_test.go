package workbench

// Helpers shared by this plugin's host tests: install a plugin archive through the real
// installer, prove the installed package did not change, wait for a build model, and save
// plugin settings the way the settings window does. They follow the Maven plugin's host
// tests (Lithe-IDEA/lithe-plugin-maven e2e/host), which drive the same internal harness.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/1lck/Lithe-IDEA/desktop/internal/core"
	"github.com/1lck/Lithe-IDEA/desktop/internal/plugininstall"
	"github.com/1lck/Lithe-IDEA/desktop/internal/pluginservice"
	"github.com/1lck/Lithe-IDEA/desktop/internal/pluginsettings"
	"github.com/1lck/Lithe-IDEA/desktop/internal/plugintrust"
)

// installOfficialPluginArchive installs and enables archive in an isolated
// plugin root and returns the installed package directory.
func installOfficialPluginArchive(t *testing.T, h *harness, archive, id string) string {
	t.Helper()
	service, err := pluginservice.Open(core.NewClient(), h.store, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The app gives its service these resolvers when it opens it (app.go); a provider or backend
	// that runs on a tool, such as a jar on java, cannot start without them, and a language
	// server whose launch uses ${logDir} cannot start without the log directory.
	service.Backend.SetToolResolver(h.app.resolveBackendTool)
	service.Backend.SetToolEntryResolver(h.app.resolveBackendToolEntry)
	service.LanguageTool = h.app.resolveLanguageTool
	service.LogDirectory = h.app.pluginLogDirectory
	h.app.plugins = service
	return installPluginArchiveInto(t, service, archive, id)
}

// installPluginArchiveInto installs and enables archive in service's plugin root, next to the
// plugins already there, and returns the installed package directory.
func installPluginArchiveInto(t *testing.T, service *pluginservice.Service, archive, id string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	plan, err := service.Installer.Prepare(ctx, plugininstall.Request{ArchivePath: archive, Root: service.Root, Source: plugintrust.Disk})
	if err != nil {
		t.Fatalf("prepare %s: %v", archive, err)
	}
	if plan.ID != id {
		_ = service.Installer.Discard(plan)
		t.Fatalf("archive holds %s, want %s", plan.ID, id)
	}
	installed, err := service.Installer.Commit(plan)
	if err != nil {
		_ = service.Installer.Discard(plan)
		t.Fatalf("install %s: %v", id, err)
	}
	if err := service.Manager.SetEnabled(id, true); err != nil {
		t.Fatal(err)
	}
	return installed.Path
}

// packageDigest hashes every file of an installed package, so a test can prove a
// workflow left the package byte-for-byte as installed.
func packageDigest(t *testing.T, root string) map[string]string {
	t.Helper()
	digest := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		// JDK module images are large; stream the unchanged-package proof so
		// test buffers do not inflate the headless host's memory observations.
		sum := sha256.New()
		_, readErr := io.Copy(sum, file)
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		digest[relative] = hex.EncodeToString(sum.Sum(nil))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func assertPackageUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := packageDigest(t, root)
	if len(after) != len(before) {
		t.Fatalf("the installed package now has %d files, it had %d", len(after), len(before))
	}
	for name, sum := range before {
		if after[name] != sum {
			t.Fatalf("the workflow changed the installed package file %s", name)
		}
	}
}

// awaitBuildModel waits until detection found an instance of buildSystemID and its
// model is loaded, failing with the model error when the plugin's command failed.
func awaitBuildModel(t *testing.T, h *harness, p *project, buildSystemID string, limit time.Duration) *buildInstance {
	t.Helper()
	var instance *buildInstance
	awaitStateWithin(t, h, limit, buildSystemID+" detection and model", func() bool {
		for index := range p.build.instances {
			candidate := &p.build.instances[index]
			if candidate.BuildSystemID != buildSystemID {
				continue
			}
			if candidate.modelError != "" {
				t.Fatalf("the %s model failed: %s", buildSystemID, candidate.modelError)
			}
			if candidate.modelLoaded {
				p.build.selected = index
				instance = candidate
				return true
			}
		}
		if p.build.failed != "" {
			t.Fatalf("build detection failed: %s", p.build.failed)
		}
		return false
	})
	return instance
}

// saveGlobalPluginSettings saves values to the plugin's global settings layer the way the
// settings window does, then dispatches the effects Core answered for the change.
func saveGlobalPluginSettings(t *testing.T, h *harness, instance *buildInstance, values map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := pluginsettings.New(h.app.kernel, h.store.Dir())
	snapshot, err := store.Load(ctx, instance.Manifest, h.app.project.root)
	if err != nil {
		t.Fatalf("load the plugin settings: %v", err)
	}
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	edits := make([]core.PluginSettingEdit, 0, len(ids))
	for _, id := range ids {
		value, err := json.Marshal(values[id])
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, core.PluginSettingEdit{ID: id, Value: value})
	}
	result, err := store.Save(ctx, snapshot, "global", edits, nil)
	if err != nil || !result.Committed {
		t.Fatalf("save the plugin settings: committed=%v err=%v", result.Committed, err)
	}
	h.app.applyPluginSettingsEffects(core.PluginSettingsOwner{PluginID: instance.PluginID, Version: instance.Version}, result.Effects)
}

// writePluginFixture writes files (relative path to content) below root.
func writePluginFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

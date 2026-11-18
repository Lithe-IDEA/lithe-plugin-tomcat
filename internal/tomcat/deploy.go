package tomcat

import (
	"archive/zip"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type DeployPlan struct {
	Artifact     string `json:"artifact"`
	Context      string `json:"context"`
	Source       string `json:"source"`
	Target       string `json:"target"`
	SourceHash   string `json:"sourceSha256"`
	ExistingHash string `json:"existingSha256,omitempty"`
	Action       string `json:"action"`
	Files        int    `json:"files,omitempty"`
	Bytes        int64  `json:"bytes,omitempty"`
	Token        string `json:"confirmationToken"`
	Warning      string `json:"warning"`
}

func shaFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, io.LimitReader(f, MaxWARSize+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// InspectDeploy performs no writes. The token binds the user's confirmation to
// the exact source bytes and destination state observed at preview time.
func InspectDeploy(checked *Checked) (*DeployPlan, error) {
	if checked.Artifact == "exploded" {
		return inspectExploded(checked)
	}
	if checked.WARPath == "" {
		return nil, errors.New("WAR not selected")
	}
	f, err := os.Open(checked.WARPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return nil, fmt.Errorf("not a valid WAR ZIP: %w", err)
	}
	if len(zr.File) == 0 || len(zr.File) > 20_000 {
		return nil, errors.New("WAR archive has an invalid entry count")
	}
	// Tomcat may unpack WAR entries after deployment. Reject traversal,
	// links, and unbounded expanded content before handing it to Tomcat.
	var expanded uint64
	for _, entry := range zr.File {
		name := entry.Name
		clean := path.Clean(name)
		if name == "" || !utf8.ValidString(name) || strings.ContainsAny(name, "\\\x00") ||
			strings.HasPrefix(name, "/") || strings.Contains(name, ":") ||
			clean == ".." || strings.HasPrefix(clean, "../") ||
			entry.FileInfo().Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("WAR contains an unsafe archive entry")
		}
		expanded += entry.UncompressedSize64
		if expanded > 2<<30 {
			return nil, errors.New("WAR expanded size exceeds the 2 GiB limit")
		}
	}
	srcHash, err := shaFile(checked.WARPath)
	if err != nil {
		return nil, err
	}
	plan := &DeployPlan{Artifact: "war", Context: checked.Context, Source: checked.WARPath, Target: checked.TargetPath, SourceHash: srcHash, Action: "create", Warning: "Deploy while Tomcat is stopped. An existing exploded deployment may be recreated by Tomcat on start."}
	st, err = os.Lstat(checked.TargetPath)
	if err == nil {
		if !st.Mode().IsRegular() {
			return nil, errors.New("existing deployment target is not a regular file")
		}
		if st.Size() > MaxWARSize {
			return nil, errors.New("existing deployment target is too large")
		}
		old, err := shaFile(checked.TargetPath)
		if err != nil {
			return nil, err
		}
		plan.ExistingHash = old
		plan.Action = "replace"
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	plan.Token = HashToken(plan.Artifact, plan.Source, plan.Target, plan.SourceHash, plan.ExistingHash, plan.Action)
	return plan, nil
}

// Deploy atomically creates a new WAR, or replaces one only after an explicit
// preview token. External changes after preview are rejected. This never
// removes an exploded directory or deploys an arbitrary filesystem path.
func Deploy(checked *Checked, confirmationToken string) (*DeployPlan, error) {
	if checked.Artifact == "exploded" {
		return deployExploded(checked, confirmationToken)
	}
	plan, err := InspectDeploy(checked)
	if err != nil {
		return nil, err
	}
	if len(confirmationToken) != len(plan.Token) || subtle.ConstantTimeCompare([]byte(confirmationToken), []byte(plan.Token)) != 1 {
		return nil, errors.New("deployment changed since preview; inspect and confirm again")
	}
	dir := checked.Webapps
	stage, err := os.CreateTemp(dir, ".lithe-tomcat-stage-*.war")
	if err != nil {
		return nil, err
	}
	stagePath := stage.Name()
	defer os.Remove(stagePath)
	if err := stage.Chmod(0644); err != nil {
		stage.Close()
		return nil, err
	}
	src, err := os.Open(checked.WARPath)
	if err != nil {
		stage.Close()
		return nil, err
	}
	copied, err := io.Copy(stage, io.LimitReader(src, MaxWARSize+1))
	closeErr := src.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil && copied > MaxWARSize {
		err = errors.New("WAR exceeded the size limit")
	}
	if err == nil {
		err = stage.Sync()
	}
	if e := stage.Close(); err == nil {
		err = e
	}
	if err != nil {
		return nil, err
	}
	newHash, err := shaFile(stagePath)
	if err != nil || newHash != plan.SourceHash {
		return nil, errors.New("source WAR changed during deployment; retry")
	}
	// Recheck target just before the write (including symlinks). Never overwrite
	// other files without a preview that includes their exact content digest.
	latest, err := InspectDeploy(checked)
	if err != nil {
		return nil, err
	}
	if latest.Token != plan.Token {
		return nil, errors.New("deployment target changed since preview; retry")
	}
	if plan.Action == "create" {
		// Atomic no-clobber creation on the same volume; os.Rename alone would
		// silently replace a new file created between inspect and commit.
		if err := os.Link(stagePath, plan.Target); err != nil {
			return nil, fmt.Errorf("cannot create WAR without overwriting: %w", err)
		}
		return plan, nil
	}
	if !strings.EqualFold(filepath.Ext(plan.Target), ".war") {
		return nil, errors.New("target is not a WAR")
	}
	backup, err := os.CreateTemp(dir, ".lithe-tomcat-backup-*.war")
	if err != nil {
		return nil, err
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		os.Remove(backupPath)
		return nil, err
	}
	if err := os.Remove(backupPath); err != nil {
		return nil, err
	}
	if err := os.Rename(plan.Target, backupPath); err != nil {
		return nil, err
	}
	if err := os.Rename(stagePath, plan.Target); err != nil {
		restoreErr := os.Rename(backupPath, plan.Target)
		if restoreErr != nil {
			return nil, fmt.Errorf("WAR install failed: %v; restoration failed: %w; backup at %s", err, restoreErr, backupPath)
		}
		return nil, err
	}
	if err := os.Remove(backupPath); err != nil {
		return nil, fmt.Errorf("WAR installed, but old backup could not be removed at %s: %w", backupPath, err)
	}
	return plan, nil
}

package tomcat

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maxExplodedFiles = 20_000
const maxExplodedDepth = 24

// treeDigest hashes both relative names and every regular file's bytes. It
// never follows links, including links that appear in a nested directory.
// The same bound applies to sources, deployed destinations and staged copies.
func treeDigest(root string) (string, int, int64, error) {
	st, err := os.Lstat(root)
	if err != nil {
		return "", 0, 0, err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", 0, 0, errors.New("exploded artifact must be a real directory")
	}
	h := sha256.New()
	files := 0
	var bytes int64
	err = filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if len(rel) > 1024 || strings.Count(rel, string(filepath.Separator)) >= maxExplodedDepth {
			return errors.New("exploded artifact path or depth exceeds limit")
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("exploded artifact contains symbolic link: %s", rel)
		}
		if strings.HasPrefix(rel, ".git"+string(filepath.Separator)) || rel == ".git" || strings.HasPrefix(rel, ".idea"+string(filepath.Separator)) || rel == ".idea" {
			return errors.New("exploded web root includes IDE or Git metadata")
		}
		normalized := filepath.ToSlash(rel)
		if e.IsDir() {
			_, _ = io.WriteString(h, "D:"+strconv.Itoa(len(normalized))+":"+normalized+"\n")
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("exploded artifact has special file: %s", rel)
		}
		files++
		if files > maxExplodedFiles || info.Size() > MaxWARSize || bytes > MaxWARSize-info.Size() {
			return errors.New("exploded artifact exceeds file count or 512 MiB size limit")
		}
		bytes += info.Size()
		_, _ = io.WriteString(h, "F:"+strconv.Itoa(len(normalized))+":"+normalized+":"+strconv.FormatInt(info.Size(), 10)+"\n")
		file, err := os.Open(p)
		if err != nil {
			return err
		}
		copied, copyErr := io.Copy(h, io.LimitReader(file, info.Size()+1))
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if copied != info.Size() {
			return errors.New("exploded source changed during hash")
		}
		return nil
	})
	if err != nil {
		return "", 0, 0, err
	}
	if files == 0 {
		return "", 0, 0, errors.New("exploded artifact has no files")
	}
	return hex.EncodeToString(h.Sum(nil)), files, bytes, nil
}

func inspectExploded(c *Checked) (*DeployPlan, error) {
	if c.ExplodedPath == "" {
		return nil, errors.New("exploded artifact was not selected")
	}
	if equalOrWithin(c.ExplodedPath, c.TargetPath) || equalOrWithin(c.TargetPath, c.ExplodedPath) {
		return nil, errors.New("exploded source overlaps the deployment destination")
	}
	if _, err := os.Lstat(strings.TrimSuffix(c.TargetPath, string(filepath.Separator)) + ".war"); err == nil {
		return nil, errors.New("a WAR with the same context exists; remove the conflict manually before exploded deployment")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	srcHash, files, bytes, err := treeDigest(c.ExplodedPath)
	if err != nil {
		return nil, err
	}
	plan := &DeployPlan{Context: c.Context, Artifact: "exploded", Source: c.ExplodedPath, Target: c.TargetPath, SourceHash: srcHash, Action: "create", Files: files, Bytes: bytes, Warning: "Do not deploy into production. A deployed directory with the same name will be replaced only after reviewing this preview."}
	if _, err := os.Lstat(c.TargetPath); err == nil {
		oldHash, _, _, err := treeDigest(c.TargetPath)
		if err != nil {
			return nil, fmt.Errorf("existing deployment: %w", err)
		}
		plan.Action = "replace"
		plan.ExistingHash = oldHash
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	plan.Token = HashToken(plan.Artifact, plan.Source, plan.Target, plan.SourceHash, plan.ExistingHash, plan.Action)
	return plan, nil
}

func equalOrWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)))
}

func copyExploded(root, dest string) error {
	return filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("artifact changed: symlink appeared")
		}
		q := filepath.Join(dest, rel)
		if e.IsDir() {
			return os.Mkdir(q, 0755)
		}
		if !info.Mode().IsRegular() {
			return errors.New("artifact changed: non-regular file appeared")
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(q, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			in.Close()
			return err
		}
		n, copyErr := io.Copy(out, io.LimitReader(in, MaxWARSize+1))
		if copyErr == nil && n > MaxWARSize {
			copyErr = errors.New("source file exceeds limit")
		}
		if copyErr == nil {
			copyErr = out.Sync()
		}
		if err := out.Close(); copyErr == nil {
			copyErr = err
		}
		if err := in.Close(); copyErr == nil {
			copyErr = err
		}
		return copyErr
	})
}

func deployExploded(c *Checked, token string) (*DeployPlan, error) {
	plan, err := inspectExploded(c)
	if err != nil {
		return nil, err
	}
	if len(token) != len(plan.Token) || subtle.ConstantTimeCompare([]byte(token), []byte(plan.Token)) != 1 {
		return nil, errors.New("deployment changed since preview; inspect and confirm again")
	}
	stage, err := os.MkdirTemp(c.Webapps, ".lithe-stage-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	if err := copyExploded(c.ExplodedPath, stage); err != nil {
		return nil, err
	}
	digest, files, bytes, err := treeDigest(stage)
	if err != nil || digest != plan.SourceHash || files != plan.Files || bytes != plan.Bytes {
		return nil, errors.New("exploded source changed during staging; retry")
	}
	latest, err := inspectExploded(c)
	if err != nil {
		return nil, err
	}
	if latest.Token != plan.Token {
		return nil, errors.New("exploded target changed since preview; retry")
	}
	if plan.Action == "create" {
		// Only the verified stage is moved. We reject an already-present
		// target; the same-volume rename is atomic when that path is absent.
		if _, err := os.Lstat(plan.Target); err == nil {
			return nil, errors.New("deployment target appeared since preview")
		}
		if err := os.Rename(stage, plan.Target); err != nil {
			return nil, err
		}
		return plan, nil
	}
	backup, err := os.MkdirTemp(c.Webapps, ".lithe-backup-")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(backup); err != nil {
		return nil, err
	}
	if err := os.Rename(plan.Target, backup); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, plan.Target); err != nil {
		if restoreErr := os.Rename(backup, plan.Target); restoreErr != nil {
			return nil, fmt.Errorf("deployment failed: %v; restore failed: %w; backup is %s", err, restoreErr, backup)
		}
		return nil, err
	}
	if err := os.RemoveAll(backup); err != nil {
		return nil, fmt.Errorf("deployment succeeded, but backup cleanup failed at %s: %w", backup, err)
	}
	return plan, nil
}

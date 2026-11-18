package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Lithe-IDEA/lithe-plugin-tomcat/internal/tomcat"
)

type deployApproval struct {
	source       string
	target       string
	artifact     string
	deployedHash string
}

var watchPollInterval = 2 * time.Second

// A watcher is private to a successfully started and approved local session.
// The opt-in setting is not sufficient: the user must first deploy manually
// and approve the exact destination. No watcher starts on plugin startup.
func (s *session) approval(runKey string, p *tomcat.DeployPlan) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approved[runKey] = deployApproval{source: p.Source, target: p.Target, artifact: p.Artifact, deployedHash: p.SourceHash}
}
func (s *session) watchStatus(runKey string) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.watching[runKey], s.watchError[runKey]
}
func (s *session) warnWatch(runKey, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watchError[runKey] = message
}
func (s *session) cancelWatch(runKey string) {
	s.mu.Lock()
	cancel := s.watches[runKey]
	s.watchGeneration[runKey]++
	delete(s.watches, runKey)
	delete(s.watching, runKey)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (s *session) canDeploy(runKey string, c tomcat.Config, checked *tomcat.Checked) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.starting[runKey] || s.deploying[runKey] || s.stopping[runKey] {
		return false
	}
	if s.runs[runKey] == "" {
		return true
	}
	return c.WatchChanges && c.Artifact == "war" && checked != nil && checked.Server.XMLValid && checked.Server.AutoDeploy
}
func (s *session) reserveDeployFor(runKey string, c tomcat.Config, checked *tomcat.Checked) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.starting[runKey] || s.deploying[runKey] || s.stopping[runKey] {
		return false
	}
	if s.runs[runKey] != "" && !(c.WatchChanges && c.Artifact == "war" && checked.Server.XMLValid && checked.Server.AutoDeploy) {
		return false
	}
	s.deploying[runKey] = true
	return true
}
func (s *session) reserveAuto(runKey, runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs[runKey] != runID || s.stopping[runKey] || s.deploying[runKey] || s.starting[runKey] {
		return false
	}
	s.deploying[runKey] = true
	return true
}

func (s *session) startWatch(runKey, runID string, c tomcat.Config, root string) string {
	if !c.WatchChanges {
		return ""
	}
	validated, err := tomcat.ValidateConfig(c)
	if err != nil {
		return err.Error()
	}
	c = validated
	if c.Artifact != "war" {
		return "Automatic deployment supports WAR files only."
	}
	checked, err := tomcat.Check(c, root, true)
	if err != nil {
		return err.Error()
	}
	if !checked.Server.XMLValid || !checked.Server.AutoDeploy {
		return "Tomcat server.xml must declare a default Host with autoDeploy enabled before live deployment."
	}
	s.mu.Lock()
	approved, known := s.approved[runKey]
	s.mu.Unlock()
	if !known || approved.source != checked.WARPath || approved.target != checked.TargetPath || approved.artifact != "war" {
		return "Confirm and deploy this WAR once before enabling automatic redeployment."
	}
	plan, err := tomcat.InspectDeploy(checked)
	if err != nil {
		return err.Error()
	}
	if plan.ExistingHash != approved.deployedHash {
		return "Installed WAR changed outside Lithe. Redeploy manually before enabling the watcher."
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.runs[runKey] != runID || s.stopping[runKey] {
		s.mu.Unlock()
		cancel()
		return "Tomcat session ended before the watcher started."
	}
	previous := s.watches[runKey]
	s.watches[runKey] = cancel
	s.watching[runKey] = true
	s.watchGeneration[runKey]++
	sequence := s.watchGeneration[runKey]
	delete(s.watchError, runKey)
	s.mu.Unlock()
	if previous != nil {
		previous()
	}
	go s.watchChanges(ctx, runKey, runID, c, root, plan.SourceHash, sequence)
	return ""
}

// Background sync is gated by a managed run, explicit opt-in, successful
// initial deployment, autoDeploy=true, stable source metadata, and an exact
// digest match for the last installed artifact. A conflict disables the
// watcher rather than overwriting external changes.
func (s *session) watchChanges(ctx context.Context, runKey, runID string, c tomcat.Config, root string, lastHash string, sequence uint64) {
	defer func() {
		s.mu.Lock()
		if s.watchGeneration[runKey] == sequence {
			delete(s.watches, runKey)
			delete(s.watching, runKey)
		}
		s.mu.Unlock()
	}()
	interval := watchPollInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastSize int64 = -1
	var lastMod time.Time
	stable := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		checked, err := tomcat.Check(c, root, true)
		if err != nil {
			s.warnWatch(runKey, fmt.Sprintf("Automatic deployment disabled: %s", err))
			return
		}
		if !checked.Server.XMLValid || !checked.Server.AutoDeploy {
			s.warnWatch(runKey, "Tomcat autoDeploy was disabled; automatic deployment stopped.")
			return
		}
		fi, err := os.Stat(checked.WARPath)
		if err != nil {
			s.warnWatch(runKey, err.Error())
			return
		}
		if fi.Size() != lastSize || !fi.ModTime().Equal(lastMod) {
			lastSize, lastMod = fi.Size(), fi.ModTime()
			stable = 0
			continue
		}
		stable++
		if stable < 1 {
			continue
		}
		// Hash only after the size and timestamp have stopped changing;
		// avoid re-reading hundreds of MiB every polling interval.
		plan, err := tomcat.InspectDeploy(checked)
		if err != nil {
			s.warnWatch(runKey, err.Error())
			return
		}
		if plan.SourceHash == lastHash {
			continue
		}
		if !s.reserveAuto(runKey, runID) {
			return
		}
		s.mu.Lock()
		approved := s.approved[runKey]
		s.mu.Unlock()
		if approved.source != plan.Source || approved.target != plan.Target || approved.deployedHash != plan.ExistingHash {
			s.finishDeploy(runKey)
			s.warnWatch(runKey, "Installed deployment changed externally; automatic deployment stopped.")
			return
		}
		deployed, err := tomcat.Deploy(checked, plan.Token)
		if err == nil {
			s.approval(runKey, deployed)
			lastHash = deployed.SourceHash
		}
		s.finishDeploy(runKey)
		if err != nil {
			s.warnWatch(runKey, fmt.Sprintf("Automatic deployment failed: %s", err))
			return
		}
	}
}

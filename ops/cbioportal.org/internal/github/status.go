package github

import (
	"context"
	"fmt"
	"net/url"
)

// Status reports a job's progress on a commit: a Check Run (shown in the
// pull request's checks) and a Deployment of a transient environment
// (shown as "deployed" with a link). Without the App nothing is posted, and
// a failed post is logged, never fatal: the job's result does not depend
// on GitHub.
type Status struct {
	C           *Client
	Repo        string // owner/name
	SHA         string
	CheckName   string // e.g. "Preview"
	Environment string // e.g. "pr-42-preview"
	Description string // of the Deployment
	LogURL      string
	Logf        func(format string, args ...any)
	// Text, when set, is the Check Run's details below its summary
	// (output.text, at most 65535 characters).
	Text string
	// DeploymentConclusion, when set, is the conclusion Finish gives the
	// Deployment instead of the Check Run's (a check can fail on something
	// other than the deployment).
	DeploymentConclusion string

	checkID, deployID int64
}

// CheckRunCreate is the body that starts a Check Run.
func CheckRunCreate(name, sha, logURL, title, summary string) map[string]any {
	return map[string]any{
		"name": name, "head_sha": sha, "status": "in_progress", "details_url": logURL,
		"output": map[string]any{"title": title, "summary": summary},
	}
}

// CheckRunProgress is the body that updates a running Check Run.
func CheckRunProgress(logURL, title, summary string) map[string]any {
	return map[string]any{
		"status": "in_progress", "details_url": logURL,
		"output": map[string]any{"title": title, "summary": summary},
	}
}

// CheckRunComplete is the body that completes a Check Run. conclusion is
// success, neutral, failure or skipped.
func CheckRunComplete(conclusion, detailsURL, title, summary string) map[string]any {
	return map[string]any{
		"status": "completed", "conclusion": conclusion, "details_url": detailsURL,
		"output": map[string]any{"title": title, "summary": summary},
	}
}

// DeploymentCreate is the body that creates a Deployment of sha to a
// transient environment, without merging or waiting for checks.
func DeploymentCreate(sha, environment, description string) map[string]any {
	return map[string]any{
		"ref": sha, "environment": environment, "auto_merge": false, "required_contexts": []string{},
		"transient_environment": true, "description": description,
	}
}

// DeploymentStatus is the body of a Deployment status. state is
// in_progress, success, failure or inactive.
func DeploymentStatus(state, envURL, logURL string) map[string]any {
	return map[string]any{"state": state, "environment_url": envURL, "log_url": logURL}
}

// DeploymentState is the Deployment state for a Check Run conclusion.
func DeploymentState(conclusion string) string {
	switch conclusion {
	case "success":
		return "success"
	case "skipped":
		return "inactive"
	}
	return "failure"
}

// withText adds Text to a Check Run body's output.
func (s *Status) withText(body map[string]any) map[string]any {
	if s.Text != "" {
		body["output"].(map[string]any)["text"] = s.Text
	}
	return body
}

func (s *Status) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// Start posts the Check Run and the Deployment (in progress, at envURL).
func (s *Status) Start(ctx context.Context, title, summary, envURL string) {
	if !s.C.HasApp() {
		return
	}
	var check, deploy struct {
		ID int64 `json:"id"`
	}
	if err := s.C.Send(ctx, "POST", "/repos/"+s.Repo+"/check-runs", CheckRunCreate(s.CheckName, s.SHA, s.LogURL, title, summary), &check); err != nil {
		s.logf("WARN: could not create the check run: %v", err)
	}
	s.checkID = check.ID
	if err := s.C.Send(ctx, "POST", "/repos/"+s.Repo+"/deployments", DeploymentCreate(s.SHA, s.Environment, s.Description), &deploy); err != nil {
		s.logf("WARN: could not create the deployment: %v", err)
	}
	s.deployID = deploy.ID
	if s.deployID != 0 {
		if err := s.C.Send(ctx, "POST", fmt.Sprintf("/repos/%s/deployments/%d/statuses", s.Repo, s.deployID), DeploymentStatus("in_progress", envURL, s.LogURL), nil); err != nil {
			s.logf("WARN: could not update deployment %d: %v", s.deployID, err)
		}
	}
	if s.checkID != 0 {
		s.logf("posted check run %d, deployment %d", s.checkID, s.deployID)
	}
}

// Progress updates the running Check Run.
func (s *Status) Progress(ctx context.Context, title, summary string) {
	if s.checkID == 0 {
		return
	}
	if err := s.C.Send(ctx, "PATCH", fmt.Sprintf("/repos/%s/check-runs/%d", s.Repo, s.checkID), s.withText(CheckRunProgress(s.LogURL, title, summary)), nil); err != nil {
		s.logf("WARN: could not update check run %d: %v", s.checkID, err)
	}
}

// Finish completes the Check Run (linking detailsURL) and sets the
// Deployment's state for the conclusion (linking envURL).
func (s *Status) Finish(ctx context.Context, conclusion, title, summary, detailsURL, envURL string) {
	if s.checkID != 0 {
		if err := s.C.Send(ctx, "PATCH", fmt.Sprintf("/repos/%s/check-runs/%d", s.Repo, s.checkID), s.withText(CheckRunComplete(conclusion, detailsURL, title, summary)), nil); err != nil {
			s.logf("WARN: could not update check run %d: %v", s.checkID, err)
		}
	}
	if s.deployID != 0 {
		if s.DeploymentConclusion != "" {
			conclusion = s.DeploymentConclusion
		}
		if err := s.C.Send(ctx, "POST", fmt.Sprintf("/repos/%s/deployments/%d/statuses", s.Repo, s.deployID), DeploymentStatus(DeploymentState(conclusion), envURL, s.LogURL), nil); err != nil {
			s.logf("WARN: could not update deployment %d: %v", s.deployID, err)
		}
	}
}

// DeactivateEnvironment marks every Deployment of an environment inactive.
func (c *Client) DeactivateEnvironment(ctx context.Context, repo, environment string) error {
	var deps []struct {
		ID int64 `json:"id"`
	}
	if err := c.Get(ctx, fmt.Sprintf("/repos/%s/deployments?environment=%s&per_page=100", repo, url.QueryEscape(environment)), &deps); err != nil {
		return err
	}
	if len(deps) > 0 && !c.HasApp() {
		return ErrNoApp
	}
	for _, d := range deps {
		if err := c.Send(ctx, "POST", fmt.Sprintf("/repos/%s/deployments/%d/statuses", repo, d.ID), map[string]any{"state": "inactive"}, nil); err != nil {
			return err
		}
	}
	return nil
}

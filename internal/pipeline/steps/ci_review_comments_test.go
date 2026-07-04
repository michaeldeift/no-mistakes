package steps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
)

// setupCIReviewCommentRepo mirrors TestCIStep_CIFailureAutoFix's git fixture:
// a bare upstream remote plus a local feature branch pushed to it, so a
// comment-driven fix can be committed and pushed through the real force-push
// safety path.
func setupCIReviewCommentRepo(t *testing.T) (dir, upstream, baseSHA, headSHA string) {
	t.Helper()
	upstream = t.TempDir()
	gitCmd(t, upstream, "init", "--bare")

	dir = t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	gitCmd(t, dir, "checkout", "-b", "main")
	os.WriteFile(filepath.Join(dir, "init.txt"), []byte("init"), 0o644)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "initial")
	baseSHA = gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "remote", "add", "origin", upstream)
	gitCmd(t, dir, "push", "origin", "main")

	gitCmd(t, dir, "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0o644)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "feature")
	headSHA = gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "push", "origin", "feature")
	return dir, upstream, baseSHA, headSHA
}

func TestCIStep_ReviewCommentAutoFix(t *testing.T) {
	t.Parallel()
	dir, upstream, baseSHA, headSHA := setupCIReviewCommentRepo(t)

	checksJSON := `[{"name":"build","state":"SUCCESS","bucket":"pass"}]`
	issueComments := `[{"id":1,"body":"Please add error handling here","created_at":"2026-01-01T00:00:00Z","user":{"login":"claude[bot]"}}]`
	env, _ := fakeCIGHWithReviewComments(t, "OPEN", checksJSON, issueComments, "[]")

	agentCalled := false
	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			agentCalled = true
			if !strings.Contains(opts.Prompt, "Please add error handling here") {
				t.Errorf("expected prompt to include review comment body, got: %s", opts.Prompt)
			}
			os.WriteFile(filepath.Join(opts.CWD, "review-fix.txt"), []byte("fixed"), 0o644)
			return &agent.Result{}, nil
		},
	}

	prURL := "https://github.com/test/repo/pull/42"
	sctx := newTestContext(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Env = env
	sctx.Run.PRURL = &prURL
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"
	sctx.Config.CITimeout = 30 * time.Second
	sctx.Config.AutoFix = config.AutoFix{CI: 3}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sctx.Ctx = ctx

	var logs []string
	sctx.Log = func(s string) { logs = append(logs, s) }

	pollCount := 0
	step := &CIStep{
		waitForNextPoll: func(ctx context.Context, interval time.Duration) error {
			pollCount++
			if pollCount == 1 {
				cancel()
			}
			return ctx.Err()
		},
	}
	_, err := step.Execute(sctx)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if !agentCalled {
		t.Fatal("expected agent to be called to address review comment")
	}

	foundDetected := false
	for _, l := range logs {
		if strings.Contains(l, "new review comment(s) detected") && strings.Contains(l, "auto-fixing") {
			foundDetected = true
		}
	}
	if !foundDetected {
		t.Errorf("expected review comment detection log, got: %v", logs)
	}

	out := gitCmd(t, upstream, "log", "feature", "--name-only", "--pretty=format:")
	if !strings.Contains(out, "review-fix.txt") {
		t.Errorf("expected review-fix.txt to be pushed to upstream feature branch, got log: %q", out)
	}
}

func TestCIStep_ReviewCommentAutoFixDedupesAlreadyProcessedComment(t *testing.T) {
	t.Parallel()
	dir, upstream, baseSHA, headSHA := setupCIReviewCommentRepo(t)

	checksJSON := `[{"name":"build","state":"SUCCESS","bucket":"pass"}]`
	issueComments := `[{"id":1,"body":"Please add error handling here","created_at":"2026-01-01T00:00:00Z","user":{"login":"claude[bot]"}}]`
	env, _ := fakeCIGHWithReviewComments(t, "OPEN", checksJSON, issueComments, "[]")

	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			os.WriteFile(filepath.Join(opts.CWD, "review-fix.txt"), []byte("fixed"), 0o644)
			return &agent.Result{}, nil
		},
	}

	prURL := "https://github.com/test/repo/pull/42"
	sctx := newTestContext(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Env = env
	sctx.Run.PRURL = &prURL
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"
	sctx.Config.CITimeout = 30 * time.Second
	sctx.Config.AutoFix = config.AutoFix{CI: 3}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sctx.Ctx = ctx
	sctx.Log = func(string) {}

	pollCount := 0
	step := &CIStep{
		waitForNextPoll: func(ctx context.Context, interval time.Duration) error {
			pollCount++
			if pollCount == 2 {
				cancel()
			}
			return ctx.Err()
		},
	}
	_, err := step.Execute(sctx)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}

	if len(ag.calls) != 1 {
		t.Fatalf("expected exactly one agent call across repeated polls of the same comment, got %d", len(ag.calls))
	}
}

func TestCIStep_ReviewCommentAutoFixSkippedWhenAutoFixDisabled(t *testing.T) {
	t.Parallel()
	dir, upstream, baseSHA, headSHA := setupCIReviewCommentRepo(t)

	checksJSON := `[{"name":"build","state":"SUCCESS","bucket":"pass"}]`
	issueComments := `[{"id":1,"body":"Please add error handling here","created_at":"2026-01-01T00:00:00Z","user":{"login":"claude[bot]"}}]`
	env, _ := fakeCIGHWithReviewComments(t, "OPEN", checksJSON, issueComments, "[]")

	ag := &mockAgent{name: "test"}

	prURL := "https://github.com/test/repo/pull/42"
	sctx := newTestContext(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Env = env
	sctx.Run.PRURL = &prURL
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"
	sctx.Config.CITimeout = 30 * time.Second
	sctx.Config.AutoFix = config.AutoFix{CI: 0}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sctx.Ctx = ctx
	sctx.Log = func(string) {}

	pollCount := 0
	step := &CIStep{
		waitForNextPoll: func(ctx context.Context, interval time.Duration) error {
			pollCount++
			cancel()
			return ctx.Err()
		},
	}
	_, err := step.Execute(sctx)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}

	if len(ag.calls) != 0 {
		t.Fatalf("expected no agent calls when auto-fix is disabled, got %d", len(ag.calls))
	}
}

func TestCIStep_ReviewCommentAutoFixIgnoredWhenHostHasNoCommentCapability(t *testing.T) {
	t.Parallel()
	dir, upstream, baseSHA, headSHA := setupCIReviewCommentRepo(t)

	checksJSON := `[{"name":"build","state":"SUCCESS","bucket":"pass"}]`
	env := fakeCIGH(t, "OPEN", checksJSON)

	ag := &mockAgent{name: "test"}

	prURL := "https://gitlab.example.com/test/repo/-/merge_requests/42"
	sctx := newTestContext(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Env = env
	sctx.Run.PRURL = &prURL
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"
	sctx.Config.CITimeout = 30 * time.Second
	sctx.Config.AutoFix = config.AutoFix{CI: 3}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sctx.Ctx = ctx
	sctx.Log = func(string) {}

	step := &CIStep{
		waitForNextPoll: func(ctx context.Context, interval time.Duration) error {
			cancel()
			return ctx.Err()
		},
	}
	_, err := step.Execute(sctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		// A GitLab host isn't wired by this fake gh CLI, so this is expected
		// to either skip cleanly or cancel - both are fine; the only thing
		// under test is that no comment-capability panic/crash occurs.
		t.Logf("Execute returned: %v", err)
	}
	if len(ag.calls) != 0 {
		t.Fatalf("expected no agent calls for a provider without CommentHost, got %d", len(ag.calls))
	}
}

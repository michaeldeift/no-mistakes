package scm

import (
	"context"
	"time"
)

// Comment is a PR comment as understood by a CommentHost implementation.
// GitHub's implementation only ever returns comments already scoped to the
// Claude review-mention bot's responses.
type Comment struct {
	ID        string
	Body      string
	CreatedAt time.Time
}

// CommentHost is an optional Host capability for providers that support
// posting a plain PR comment and reading back comments left in response to
// it. It is deliberately not part of the required Host interface: only
// GitHub implements it today (it drives the @claude-mention review-and-fix
// loop), and adding it to Host would force GitLab, Bitbucket, and Azure
// DevOps to stub out something they don't need. Callers must type-assert
// (host.(CommentHost)) rather than calling it unconditionally.
type CommentHost interface {
	// PostComment posts a plain top-level comment on the PR.
	PostComment(ctx context.Context, pr *PR, body string) error

	// ListReviewResponseComments returns comments left in response to a
	// review request. Order is not guaranteed; callers should sort by
	// CreatedAt if ordering matters.
	ListReviewResponseComments(ctx context.Context, pr *PR) ([]Comment, error)
}

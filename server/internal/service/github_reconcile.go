package service

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type GitHubReconcileService struct {
	Queries    *db.Queries
	HTTPClient *http.Client
}

func NewGitHubReconcileService(q *db.Queries) *GitHubReconcileService {
	return &GitHubReconcileService{
		Queries:    q,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func NormalizeGitHubRepo(repo string) string {
	s := strings.TrimSpace(repo)
	s = strings.TrimPrefix(s, "https://github.com/")
	s = strings.TrimPrefix(s, "http://github.com/")
	s = strings.TrimPrefix(s, "github.com/")
	s = strings.TrimPrefix(s, "git@github.com:")
	s = strings.TrimSuffix(s, ".git")
	return strings.Trim(s, "/")
}

func (s *GitHubReconcileService) UpsertIssuePRLinkAndSyncIssue(ctx context.Context, issue db.Issue, repo string, prNumber int32, prURL, state string) (db.IssuePrLink, *db.Issue, error) {
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	row, err := s.Queries.UpsertIssuePRLink(ctx, db.UpsertIssuePRLinkParams{
		IssueID:    issue.ID,
		GithubRepo: repo,
		PrNumber:   prNumber,
		PrUrl:      strings.TrimSpace(prURL),
		PrState:    state,
		MergedAt: func() pgtype.Timestamptz {
			if state == "merged" {
				return now
			}
			return pgtype.Timestamptz{}
		}(),
		ClosedAt: func() pgtype.Timestamptz {
			if state == "closed" || state == "merged" {
				return now
			}
			return pgtype.Timestamptz{}
		}(),
	})
	if err != nil {
		return db.IssuePrLink{}, nil, err
	}

	// The canonical GitHub linkage is the issue_pr_link table. The current
	// issue update query no longer carries GitHub link columns directly, so
	// return the upserted link and let callers refresh issue state as needed.
	return row, nil, nil
}

func (s *GitHubReconcileService) IssueHasMergedPR(ctx context.Context, issueID pgtype.UUID) (bool, error) {
	links, err := s.Queries.ListIssuePRLinks(ctx, issueID)
	if err != nil {
		return false, err
	}
	for _, link := range links {
		if strings.EqualFold(link.PrState, "merged") {
			return true, nil
		}
	}
	return false, nil
}

func (s *GitHubReconcileService) CloseLinkedGitHubIssueBestEffort(issue db.Issue) {
	token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if token == "" || !issue.GithubRepo.Valid || !issue.GithubIssueNumber.Valid {
		return
	}
	url := "https://api.github.com/repos/" + NormalizeGitHubRepo(issue.GithubRepo.String) + "/issues/" + strconv.FormatInt(int64(issue.GithubIssueNumber.Int32), 10)
	body := []byte(`{"state":"closed"}`)
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
}

package handler

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type IssuePRLinkResponse struct {
	ID         string  `json:"id"`
	IssueID    string  `json:"issue_id"`
	GithubRepo string  `json:"github_repo"`
	PrNumber   int32   `json:"pr_number"`
	PrURL      string  `json:"pr_url"`
	PrState    string  `json:"pr_state"`
	MergedAt   *string `json:"merged_at"`
	ClosedAt   *string `json:"closed_at"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

func issuePRLinkToResponse(row db.IssuePrLink) IssuePRLinkResponse {
	return IssuePRLinkResponse{
		ID:         uuidToString(row.ID),
		IssueID:    uuidToString(row.IssueID),
		GithubRepo: row.GithubRepo,
		PrNumber:   row.PrNumber,
		PrURL:      row.PrUrl,
		PrState:    row.PrState,
		MergedAt:   timestampToPtr(row.MergedAt),
		ClosedAt:   timestampToPtr(row.ClosedAt),
		CreatedAt:  timestampToString(row.CreatedAt),
		UpdatedAt:  timestampToString(row.UpdatedAt),
	}
}

type UpsertIssuePRLinkRequest struct {
	GithubRepo string `json:"github_repo"`
	PrNumber   int32  `json:"pr_number"`
	PrURL      string `json:"pr_url"`
	PrState    string `json:"pr_state,omitempty"`
}

func normalizeGitHubRepo(repo string) string {
	s := strings.TrimSpace(repo)
	s = strings.TrimPrefix(s, "https://github.com/")
	s = strings.TrimPrefix(s, "http://github.com/")
	s = strings.TrimPrefix(s, "github.com/")
	s = strings.TrimPrefix(s, "git@github.com:")
	s = strings.TrimSuffix(s, ".git")
	return strings.Trim(s, "/")
}

func (h *Handler) ListIssuePRLinks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}
	rows, err := h.Queries.ListIssuePRLinks(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list PR links")
		return
	}
	resp := make([]IssuePRLinkResponse, len(rows))
	for i, row := range rows {
		resp[i] = issuePRLinkToResponse(row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp})
}

func (h *Handler) UpsertIssuePRLink(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}
	var req UpsertIssuePRLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	repo := normalizeGitHubRepo(req.GithubRepo)
	if repo == "" || req.PrNumber <= 0 || strings.TrimSpace(req.PrURL) == "" {
		writeError(w, http.StatusBadRequest, "github_repo, pr_number, pr_url are required")
		return
	}
	state := req.PrState
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "closed" && state != "merged" {
		writeError(w, http.StatusBadRequest, "pr_state must be open, closed, or merged")
		return
	}
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	row, err := h.Queries.UpsertIssuePRLink(r.Context(), db.UpsertIssuePRLinkParams{
		IssueID:    issue.ID,
		GithubRepo: repo,
		PrNumber:   req.PrNumber,
		PrUrl:      strings.TrimSpace(req.PrURL),
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
		writeError(w, http.StatusInternalServerError, "failed to save PR link")
		return
	}

	// Keep issue-level GitHub metadata in sync for downstream automations.
	updatedIssue, err := h.Queries.UpdateIssue(r.Context(), db.UpdateIssueParams{
		ID:                issue.ID,
		AssigneeType:      issue.AssigneeType,
		AssigneeID:        issue.AssigneeID,
		DueDate:           issue.DueDate,
		ParentIssueID:     issue.ParentIssueID,
		ProjectID:         issue.ProjectID,
		GithubRepo:        pgtype.Text{String: repo, Valid: true},
		GithubPrNumber:    pgtype.Int4{Int32: req.PrNumber, Valid: true},
		GithubIssueNumber: issue.GithubIssueNumber,
	})
	if err == nil {
		prefix := h.getIssuePrefix(r.Context(), updatedIssue.WorkspaceID)
		h.publish(protocol.EventIssueUpdated, uuidToString(updatedIssue.WorkspaceID), "system", "", map[string]any{
			"issue": issueToResponse(updatedIssue, prefix),
		})
	}

	writeJSON(w, http.StatusOK, issuePRLinkToResponse(row))
}

type githubPullRequestWebhook struct {
	Action     string `json:"action"`
	Number     int32  `json:"number"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Number   int32  `json:"number"`
		State    string `json:"state"`
		Merged   bool   `json:"merged"`
		HTMLURL  string `json:"html_url"`
		MergedAt string `json:"merged_at"`
		ClosedAt string `json:"closed_at"`
	} `json:"pull_request"`
}

func verifyGitHubWebhook(body []byte, signatureHeader, secret string) bool {
	if secret == "" {
		return true
	}
	if !strings.HasPrefix(signatureHeader, "sha256=") {
		return false
	}
	expectedSig := strings.TrimPrefix(signatureHeader, "sha256=")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	computed := mac.Sum(nil)
	expected, err := hex.DecodeString(expectedSig)
	if err != nil {
		return false
	}
	return hmac.Equal(computed, expected)
}

func (h *Handler) HandleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	eventType := strings.TrimSpace(r.Header.Get("X-GitHub-Event"))
	if eventType != "pull_request" {
		writeJSON(w, http.StatusOK, map[string]any{"ignored": true})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	secret := strings.TrimSpace(os.Getenv("GITHUB_WEBHOOK_SECRET"))
	if !verifyGitHubWebhook(body, r.Header.Get("X-Hub-Signature-256"), secret) {
		writeError(w, http.StatusUnauthorized, "invalid webhook signature")
		return
	}
	var payload githubPullRequestWebhook
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid webhook payload")
		return
	}
	if payload.Action != "closed" {
		writeJSON(w, http.StatusOK, map[string]any{"ignored": true, "reason": "action_not_closed"})
		return
	}
	repo := normalizeGitHubRepo(payload.Repository.FullName)
	prNumber := payload.PullRequest.Number
	if prNumber == 0 {
		prNumber = payload.Number
	}
	link, err := h.Queries.GetIssuePRLinkByRepoAndNumber(r.Context(), db.GetIssuePRLinkByRepoAndNumberParams{
		GithubRepo: repo,
		PrNumber:   prNumber,
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ignored": true, "reason": "link_not_found"})
		return
	}
	mergedAt := pgtype.Timestamptz{}
	closedAt := pgtype.Timestamptz{}
	if t, err := time.Parse(time.RFC3339, payload.PullRequest.ClosedAt); err == nil {
		closedAt = pgtype.Timestamptz{Time: t.UTC(), Valid: true}
	}
	if payload.PullRequest.Merged {
		if t, err := time.Parse(time.RFC3339, payload.PullRequest.MergedAt); err == nil {
			mergedAt = pgtype.Timestamptz{Time: t.UTC(), Valid: true}
		}
	}
	_, _ = h.Queries.UpsertIssuePRLink(r.Context(), db.UpsertIssuePRLinkParams{
		IssueID:    link.IssueID,
		GithubRepo: repo,
		PrNumber:   prNumber,
		PrUrl:      payload.PullRequest.HTMLURL,
		PrState: func() string {
			if payload.PullRequest.Merged {
				return "merged"
			}
			return "closed"
		}(),
		MergedAt: mergedAt,
		ClosedAt: closedAt,
	})

	if payload.PullRequest.Merged {
		issue, err := h.Queries.GetIssue(r.Context(), link.IssueID)
		if err == nil {
			targetStatus, resolveErr := h.resolveIssueStatusByDependencies(r.Context(), issue, "done")
			if resolveErr == nil {
				updated, updateErr := h.Queries.UpdateIssue(r.Context(), db.UpdateIssueParams{
					ID:                issue.ID,
					Status:            pgtype.Text{String: targetStatus, Valid: true},
					AssigneeType:      issue.AssigneeType,
					AssigneeID:        issue.AssigneeID,
					DueDate:           issue.DueDate,
					ParentIssueID:     issue.ParentIssueID,
					ProjectID:         issue.ProjectID,
					GithubRepo:        issue.GithubRepo,
					GithubIssueNumber: issue.GithubIssueNumber,
					GithubPrNumber:    issue.GithubPrNumber,
				})
				if updateErr == nil {
					prefix := h.getIssuePrefix(r.Context(), updated.WorkspaceID)
					h.publish(protocol.EventIssueUpdated, uuidToString(updated.WorkspaceID), "system", "", map[string]any{
						"issue": issueToResponse(updated, prefix),
					})
					h.closeLinkedGitHubIssueBestEffort(updated)
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) closeLinkedGitHubIssueBestEffort(issue db.Issue) {
	token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if token == "" || !issue.GithubRepo.Valid || !issue.GithubIssueNumber.Valid {
		return
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d", normalizeGitHubRepo(issue.GithubRepo.String), issue.GithubIssueNumber.Int32)
	body := []byte(`{"state":"closed"}`)
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
}

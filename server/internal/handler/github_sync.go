package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
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
	repo := service.NormalizeGitHubRepo(req.GithubRepo)
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
	row, updatedIssue, err := h.GitHubSync.UpsertIssuePRLinkAndSyncIssue(r.Context(), issue, repo, req.PrNumber, req.PrURL, state)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save PR link")
		return
	}

	if updatedIssue != nil {
		prefix := h.getIssuePrefix(r.Context(), updatedIssue.WorkspaceID)
		h.publish(protocol.EventIssueUpdated, uuidToString(updatedIssue.WorkspaceID), "system", "", map[string]any{
			"issue": issueToResponse(*updatedIssue, prefix),
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
	repo := service.NormalizeGitHubRepo(payload.Repository.FullName)
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
	state := "closed"
	if payload.PullRequest.Merged {
		state = "merged"
	}
	issue, issueErr := h.Queries.GetIssue(r.Context(), link.IssueID)
	if issueErr == nil {
		_, _, _ = h.GitHubSync.UpsertIssuePRLinkAndSyncIssue(r.Context(), issue, repo, prNumber, payload.PullRequest.HTMLURL, state)
	} else {
		_, _ = h.Queries.UpsertIssuePRLink(r.Context(), db.UpsertIssuePRLinkParams{
			IssueID:    link.IssueID,
			GithubRepo: repo,
			PrNumber:   prNumber,
			PrUrl:      payload.PullRequest.HTMLURL,
			PrState:    state,
			MergedAt:   mergedAt,
			ClosedAt:   closedAt,
		})
	}

	if payload.PullRequest.Merged {
		if issueErr == nil {
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
					h.GitHubSync.CloseLinkedGitHubIssueBestEffort(updated)
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

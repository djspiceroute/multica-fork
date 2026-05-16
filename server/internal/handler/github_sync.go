package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
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

	if state == "merged" && issue.Status != "done" {
		updated, uerr := h.Queries.UpdateIssueStatus(r.Context(), db.UpdateIssueStatusParams{
			ID:     issue.ID,
			Status: "done",
		})
		if uerr == nil {
			prefix := h.getIssuePrefix(r.Context(), updated.WorkspaceID)
			h.publish(protocol.EventIssueUpdated, uuidToString(updated.WorkspaceID), "system", "", map[string]any{
				"issue": issueToResponse(updated, prefix),
			})
		}
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
		if err == pgx.ErrNoRows {
			writeJSON(w, http.StatusOK, map[string]any{"ignored": true, "reason": "link_not_found"})
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to resolve PR link")
		return
	}

	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	state := "closed"
	if payload.PullRequest.Merged {
		state = "merged"
	}
	_, _ = h.Queries.UpsertIssuePRLink(r.Context(), db.UpsertIssuePRLinkParams{
		IssueID:    link.IssueID,
		GithubRepo: repo,
		PrNumber:   prNumber,
		PrUrl:      payload.PullRequest.HTMLURL,
		PrState:    state,
		MergedAt: func() pgtype.Timestamptz {
			if state == "merged" {
				return now
			}
			return pgtype.Timestamptz{}
		}(),
		ClosedAt: now,
	})

	if payload.PullRequest.Merged {
		issue, issueErr := h.Queries.GetIssue(r.Context(), link.IssueID)
		if issueErr == nil && issue.Status != "done" {
			updated, updateErr := h.Queries.UpdateIssueStatus(r.Context(), db.UpdateIssueStatusParams{
				ID:     issue.ID,
				Status: "done",
			})
			if updateErr == nil {
				prefix := h.getIssuePrefix(r.Context(), updated.WorkspaceID)
				h.publish(protocol.EventIssueUpdated, uuidToString(updated.WorkspaceID), "system", "", map[string]any{
					"issue": issueToResponse(updated, prefix),
				})
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type SyncGitHubIssuesRequest struct {
	GithubRepo string `json:"github_repo"`
	State      string `json:"state,omitempty"` // open|all
}

type githubIssueDTO struct {
	Number      int32  `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	State       string `json:"state"`
	HTMLURL     string `json:"html_url"`
	PullRequest any    `json:"pull_request,omitempty"`
}

func githubIssueStateToMulticaStatus(state string) string {
	if strings.EqualFold(strings.TrimSpace(state), "closed") {
		return "done"
	}
	return "todo"
}

func buildGitHubIssueDescription(url, body string) string {
	b := strings.TrimSpace(body)
	source := fmt.Sprintf("Imported from GitHub: %s", strings.TrimSpace(url))
	if b == "" {
		return source
	}
	return source + "\n\n" + b
}

func (h *Handler) createGitHubIssue(
	r *http.Request,
	workspaceID string,
	creatorID string,
	repo string,
	gi githubIssueDTO,
) (db.Issue, error) {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		return db.Issue{}, err
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	issueNumber, err := qtx.IncrementIssueCounter(r.Context(), parseUUID(workspaceID))
	if err != nil {
		return db.Issue{}, err
	}
	issue, err := qtx.CreateIssue(r.Context(), db.CreateIssueParams{
		WorkspaceID: parseUUID(workspaceID),
		Title:       strings.TrimSpace(gi.Title),
		Description: strToText(buildGitHubIssueDescription(gi.HTMLURL, gi.Body)),
		Status:      githubIssueStateToMulticaStatus(gi.State),
		Priority:    "none",
		CreatorType: "member",
		CreatorID:   parseUUID(creatorID),
		Position:    0,
		Number:      issueNumber,
	})
	if err != nil {
		return db.Issue{}, err
	}
	issue, err = qtx.UpdateIssueFromGitHub(r.Context(), db.UpdateIssueFromGitHubParams{
		ID:                issue.ID,
		Title:             strings.TrimSpace(gi.Title),
		Description:       strToText(buildGitHubIssueDescription(gi.HTMLURL, gi.Body)),
		Status:            githubIssueStateToMulticaStatus(gi.State),
		GithubRepo:        strToText(repo),
		GithubIssueNumber: pgtype.Int4{Int32: gi.Number, Valid: true},
	})
	if err != nil {
		return db.Issue{}, err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return db.Issue{}, err
	}
	return issue, nil
}

func (h *Handler) SyncGitHubIssues(w http.ResponseWriter, r *http.Request) {
	var req SyncGitHubIssuesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	repo := service.NormalizeGitHubRepo(req.GithubRepo)
	if repo == "" {
		writeError(w, http.StatusBadRequest, "github_repo is required")
		return
	}
	state := strings.ToLower(strings.TrimSpace(req.State))
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "all" {
		writeError(w, http.StatusBadRequest, "state must be open or all")
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	client := &http.Client{Timeout: 20 * time.Second}
	const perPage = 100
	var fetched []githubIssueDTO
	for page := 1; page <= 10; page++ {
		url := fmt.Sprintf("https://api.github.com/repos/%s/issues?state=%s&per_page=%d&page=%d", repo, state, perPage, page)
		httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create github request")
			return
		}
		httpReq.Header.Set("Accept", "application/vnd.github+json")
		if token != "" {
			httpReq.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := client.Do(httpReq)
		if err != nil {
			writeError(w, http.StatusBadGateway, "failed to fetch github issues")
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			writeError(w, http.StatusBadGateway, "github api error: "+strconv.Itoa(resp.StatusCode))
			return
		}
		var pageItems []githubIssueDTO
		if err := json.Unmarshal(body, &pageItems); err != nil {
			writeError(w, http.StatusBadGateway, "invalid github issues response")
			return
		}
		if len(pageItems) == 0 {
			break
		}
		fetched = append(fetched, pageItems...)
		if len(pageItems) < perPage {
			break
		}
	}

	created := 0
	updated := 0
	skipped := 0
	for _, gi := range fetched {
		if gi.PullRequest != nil || gi.Number <= 0 || strings.TrimSpace(gi.Title) == "" {
			skipped++
			continue
		}
		existing, err := h.Queries.GetIssueByGitHubIssueInWorkspace(r.Context(), db.GetIssueByGitHubIssueInWorkspaceParams{
			WorkspaceID:       parseUUID(workspaceID),
			GithubRepo:        strToText(repo),
			GithubIssueNumber: pgtype.Int4{Int32: gi.Number, Valid: true},
		})
		if err == nil {
			_, err = h.Queries.UpdateIssueFromGitHub(r.Context(), db.UpdateIssueFromGitHubParams{
				ID:                existing.ID,
				Title:             strings.TrimSpace(gi.Title),
				Description:       strToText(buildGitHubIssueDescription(gi.HTMLURL, gi.Body)),
				Status:            githubIssueStateToMulticaStatus(gi.State),
				GithubRepo:        strToText(repo),
				GithubIssueNumber: pgtype.Int4{Int32: gi.Number, Valid: true},
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to update imported issue")
				return
			}
			updated++
			continue
		}
		if err != pgx.ErrNoRows {
			writeError(w, http.StatusInternalServerError, "failed to query existing imported issue")
			return
		}
		issue, err := h.createGitHubIssue(r, workspaceID, userID, repo, gi)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create imported issue")
			return
		}
		prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
		h.publish(protocol.EventIssueCreated, workspaceID, "member", userID, map[string]any{
			"issue": issueToResponse(issue, prefix),
		})
		created++
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"repo":    repo,
		"state":   state,
		"fetched": len(fetched),
		"created": created,
		"updated": updated,
		"skipped": skipped,
	})
}

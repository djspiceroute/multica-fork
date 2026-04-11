-- name: UpsertIssuePRLink :one
INSERT INTO issue_pr_link (
    issue_id, github_repo, pr_number, pr_url, pr_state, merged_at, closed_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
ON CONFLICT (github_repo, pr_number) DO UPDATE SET
    issue_id = EXCLUDED.issue_id,
    pr_url = EXCLUDED.pr_url,
    pr_state = EXCLUDED.pr_state,
    merged_at = EXCLUDED.merged_at,
    closed_at = EXCLUDED.closed_at,
    updated_at = now()
RETURNING *;

-- name: ListIssuePRLinks :many
SELECT * FROM issue_pr_link
WHERE issue_id = $1
ORDER BY created_at DESC;

-- name: GetIssuePRLinkByRepoAndNumber :one
SELECT * FROM issue_pr_link
WHERE github_repo = $1 AND pr_number = $2;

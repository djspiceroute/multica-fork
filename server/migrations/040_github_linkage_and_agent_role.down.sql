ALTER TABLE agent
    DROP CONSTRAINT IF EXISTS agent_role_check,
    DROP COLUMN IF EXISTS role;

DROP INDEX IF EXISTS idx_issue_pr_link_repo_pr;
DROP INDEX IF EXISTS idx_issue_pr_link_issue_id;
DROP TABLE IF EXISTS issue_pr_link;

DROP INDEX IF EXISTS idx_issue_github_repo_pr_number;
DROP INDEX IF EXISTS idx_issue_github_repo_issue_number;

ALTER TABLE issue
    DROP COLUMN IF EXISTS github_pr_number,
    DROP COLUMN IF EXISTS github_issue_number,
    DROP COLUMN IF EXISTS github_repo;

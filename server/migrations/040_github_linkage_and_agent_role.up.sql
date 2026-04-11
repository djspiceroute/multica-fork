ALTER TABLE issue
    ADD COLUMN github_repo text,
    ADD COLUMN github_issue_number int,
    ADD COLUMN github_pr_number int;

CREATE INDEX idx_issue_github_repo_issue_number
    ON issue (github_repo, github_issue_number)
    WHERE github_repo IS NOT NULL AND github_issue_number IS NOT NULL;

CREATE INDEX idx_issue_github_repo_pr_number
    ON issue (github_repo, github_pr_number)
    WHERE github_repo IS NOT NULL AND github_pr_number IS NOT NULL;

CREATE TABLE issue_pr_link (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    issue_id uuid NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    github_repo text NOT NULL,
    pr_number int NOT NULL,
    pr_url text NOT NULL,
    pr_state text NOT NULL DEFAULT 'open',
    merged_at timestamptz,
    closed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT issue_pr_link_pr_state_check CHECK (pr_state IN ('open', 'closed', 'merged')),
    CONSTRAINT uq_issue_pr_link_repo_pr UNIQUE (github_repo, pr_number)
);

CREATE INDEX idx_issue_pr_link_issue_id ON issue_pr_link(issue_id);
CREATE INDEX idx_issue_pr_link_repo_pr ON issue_pr_link(github_repo, pr_number);

ALTER TABLE agent
    ADD COLUMN role text NOT NULL DEFAULT 'coder',
    ADD CONSTRAINT agent_role_check CHECK (role IN ('coder', 'reviewer'));

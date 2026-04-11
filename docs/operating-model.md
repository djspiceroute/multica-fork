# Multica Issue/PR Operating Model

## Goal

Use `In Review` and `Done` as reliable delivery states tied to GitHub lifecycle.

## Status Model

- `todo` / `in_progress`: coding phase.
- `in_review`: PR is open and ready for review.
- `done`: PR merged to `main` (or equivalent default branch), and issue is complete.
- `blocked`: dependency or execution blocker.

## Agent Roles

Multica agents now support:

- `coder`: implementation owner (default role).
- `reviewer`: review owner for `in_review` issues.

Assignment behavior:

- Issue assignee picker only shows `coder` agents for implementation assignment.
- When an issue is moved to `in_review`, Multica auto-handoffs assignment from coder to the first active reviewer (if present).

## GitHub Linkage

Issue now stores:

- `github_repo` (e.g. `owner/repo`)
- `github_issue_number`
- `github_pr_number`

PR linkage is tracked in `issue_pr_link`:

- `issue_id`, `github_repo`, `pr_number`, `pr_url`, `pr_state`, `merged_at`, `closed_at`.

## Recommended Workflow

1. Coder agent completes work and opens PR.
2. Coder sets issue `in_review` and records PR link via issue GitHub link API.
3. Reviewer agent/human reviews PR.
4. PR merges to `main`.
5. GitHub webhook (`pull_request` closed+merged) marks issue `done` automatically.
6. If issue has a linked GitHub issue number and token is configured, Multica attempts to close it.

## Required Configuration

Set these env vars on backend:

- `GITHUB_WEBHOOK_SECRET` (optional but strongly recommended): validates webhook signature.
- `GITHUB_TOKEN` (optional): enables auto-close of linked GitHub issues when issue reaches `done`.

Webhook endpoint:

- `POST /webhooks/github`
- Expected event header: `X-GitHub-Event: pull_request`

## Human Review Checklist (lightweight)

Before merge:

- Scope matches issue intent.
- CI is green.
- No surprise sensitive file changes.
- Test evidence exists.
- Rollback is clear.

Merge authority should stay with human reviewer by default.

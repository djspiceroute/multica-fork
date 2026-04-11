#!/usr/bin/env bash
set -euo pipefail

# Guarded reviewer merge:
# - requires label (default: automerge-ok)
# - requires all checks passing
# - requires mergeable/no conflicts
# - merges using configured strategy (default: squash)
#
# Usage:
#   bash scripts/reviewer-automerge.sh <pr-number-or-url>

if ! command -v gh >/dev/null 2>&1; then
  echo "gh CLI not found on PATH" >&2
  exit 1
fi

TARGET="${1:-}"
if [ -z "$TARGET" ]; then
  echo "usage: bash scripts/reviewer-automerge.sh <pr-number-or-url>" >&2
  exit 1
fi

LABEL="${MULTICA_AUTOMERGE_LABEL:-automerge-ok}"
STRATEGY="${MULTICA_AUTOMERGE_STRATEGY:-squash}"
ISSUE_ID="${MULTICA_ISSUE_ID:-}"

case "$STRATEGY" in
  squash) MERGE_FLAG="--squash" ;;
  merge) MERGE_FLAG="--merge" ;;
  rebase) MERGE_FLAG="--rebase" ;;
  *)
    echo "invalid MULTICA_AUTOMERGE_STRATEGY: $STRATEGY (expected squash|merge|rebase)" >&2
    exit 1
    ;;
esac

audit_comment() {
  local content="$1"
  if [ -n "$ISSUE_ID" ] && command -v multica >/dev/null 2>&1; then
    multica issue comment add "$ISSUE_ID" --content "$content" >/dev/null 2>&1 || true
  fi
}

labels_csv="$(gh pr view "$TARGET" --json labels --jq '[.labels[].name] | join(",")')"
if ! echo ",$labels_csv," | grep -qi ",$LABEL,"; then
  msg="[automerge audit] blocked: missing required label \`$LABEL\` on PR \`$TARGET\`."
  echo "$msg" >&2
  audit_comment "$msg"
  exit 2
fi

merge_state="$(gh pr view "$TARGET" --json mergeStateStatus --jq '.mergeStateStatus')"
if [ "$merge_state" = "DIRTY" ] || [ "$merge_state" = "CONFLICTING" ] || [ "$merge_state" = "BLOCKED" ]; then
  msg="[automerge audit] blocked: PR \`$TARGET\` is not mergeable (\`$merge_state\`). Resolve conflicts/blocks first."
  echo "$msg" >&2
  audit_comment "$msg"
  exit 3
fi

checks_pending_or_failed="$(
  gh pr view "$TARGET" --json statusCheckRollup --jq '
    ([.statusCheckRollup[]? |
      if has("conclusion") then
        (.conclusion // "PENDING")
      else
        (.state // "PENDING")
      end
    ] | map(select(. != "SUCCESS" and . != "NEUTRAL" and . != "SKIPPED")) | length)
  '
)"

if [ "${checks_pending_or_failed:-0}" -gt 0 ]; then
  msg="[automerge audit] blocked: PR \`$TARGET\` has non-green checks (count=${checks_pending_or_failed})."
  echo "$msg" >&2
  audit_comment "$msg"
  exit 4
fi

echo "All gates passed for $TARGET (label=$LABEL, mergeStateStatus=$merge_state, checks=green)."
audit_comment "[automerge audit] allowed: all merge gates passed for PR \`$TARGET\` (label=\`$LABEL\`, strategy=\`$STRATEGY\`)."

gh pr merge "$TARGET" $MERGE_FLAG --delete-branch

audit_comment "[automerge audit] merged: PR \`$TARGET\` merged successfully via reviewer guard (\`$STRATEGY\`)."
echo "Merged $TARGET with strategy=$STRATEGY"

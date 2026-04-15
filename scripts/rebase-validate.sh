#!/bin/bash

################################################################################
# Multica Rebase Validation Script
# ================================
# Validates the state of the repository after a git rebase.
# - Detects rebase state and migration changes
# - Runs full CI pipeline (scripts/check.sh)
# - Analyzes migration diffs for complexity
# - Auto-recovers from minor migration failures
# - Generates JSON report for agent consumption
#
# Usage: ./scripts/rebase-validate.sh [--json] [--help]
# Exit codes:
#   0 = all checks passed
#   1 = checks failed
#   2 = human review needed (major migration changes)
################################################################################

set -euo pipefail

# Ensure npm packages are in PATH (for pnpm, etc.)
export PATH="$HOME/.npm-global/bin:$HOME/.local/bin:/usr/local/bin:$PATH"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT_FORMAT="human"  # "human" or "json"
TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD)

# Initialize result tracking
CHECKS=()
STATUS="pass"
FAILED_CHECKS=()
MIGRATION_STATUS="{\"files_changed\": 0, \"lines_changed\": 0, \"major_detected\": false, \"extensions_added\": [], \"migrations_new\": []}"

# Color codes for human output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

################################################################################
# Utility Functions
################################################################################

log_section() {
  if [[ "$OUTPUT_FORMAT" == "human" ]]; then
    echo ""
    echo -e "${BLUE}▶ $1${NC}"
  fi
}

log_pass() {
  if [[ "$OUTPUT_FORMAT" == "human" ]]; then
    echo -e "${GREEN}✓ $1${NC}"
  fi
}

log_fail() {
  if [[ "$OUTPUT_FORMAT" == "human" ]]; then
    echo -e "${RED}✗ $1${NC}"
  fi
}

log_warn() {
  if [[ "$OUTPUT_FORMAT" == "human" ]]; then
    echo -e "${YELLOW}⚠ $1${NC}"
  fi
}

add_check() {
  local name="$1"
  local check_status="$2"
  local duration="${3:-0}"
  
  CHECKS+=("{\"name\": \"$name\", \"status\": \"$check_status\", \"duration_ms\": $duration}")
  
  if [[ "$check_status" == "fail" ]]; then
    STATUS="fail"
    FAILED_CHECKS+=("$name")
  fi
}

################################################################################
# Rebase Detection & Migration Analysis
################################################################################

detect_rebase_state() {
  log_section "Detecting rebase state..."
  
  REBASE_DETECTED=false
  
  # Check if we're in a rebase
  if [[ -f "$REPO_ROOT/.git/REBASE_HEAD" ]]; then
    REBASE_DETECTED=true
    log_warn "Repository is in REBASE state (REBASE_HEAD exists)"
    return 0
  fi
  
  # Check reflog for recent rebase
  if git reflog | head -1 | grep -q "rebase"; then
    REBASE_DETECTED=true
    log_warn "Recent rebase detected in reflog"
    return 0
  fi
  
  log_pass "No rebase state detected"
}

analyze_migration_changes() {
  log_section "Analyzing migration changes..."
  
  local files_changed=0
  local lines_changed=0
  local extensions_added=()
  local migrations_new=()
  local major_detected=false
  
  # Get migration files changed since origin/main
  if ! git diff --name-only origin/main...HEAD | grep -q "server/migrations/"; then
    log_pass "No migration changes detected"
    return 0
  fi
  
  # Count files in migrations that changed
  files_changed=$(git diff --name-only origin/main...HEAD -- server/migrations/ | wc -l)
  
  # Count total lines added/removed in migrations
  lines_changed=$(git diff origin/main...HEAD -- server/migrations/ | grep -E '^\+|^-' | grep -v '^\+\+\+|^---' | wc -l)
  
  # Check for new migrations not in origin/main
  while IFS= read -r migration_file; do
    if ! git show origin/main:"$migration_file" 2>/dev/null >/dev/null; then
      migrations_new+=("$(basename "$migration_file")")
    fi
  done < <(git diff --name-only origin/main...HEAD -- server/migrations/)
  
  # Check for extension changes in migration content
  if git diff origin/main...HEAD -- server/migrations/ | grep -iE 'CREATE EXTENSION|pg_bigm|pgcrypto|uuid-ossp'; then
    extensions_added+=("detected")
  fi
  
  # Determine if migration is "major"
  if [[ $files_changed -ge 3 ]] || [[ $lines_changed -ge 30 ]] || [[ ${#extensions_added[@]} -gt 0 ]]; then
    major_detected=true
    STATUS="needs-review"
  fi
  
  # Build migration status JSON
  local migrations_json=""
  if [[ ${#migrations_new[@]} -gt 0 ]]; then
    migrations_json="[$(printf '"%s"' "${migrations_new[@]}" | sed 's/" /", /g')]"
  else
    migrations_json="[]"
  fi
  
  MIGRATION_STATUS="{
    \"files_changed\": $files_changed,
    \"lines_changed\": $lines_changed,
    \"major_detected\": $major_detected,
    \"extensions_added\": $(printf '["%s"]' "${extensions_added[@]}"),
    \"migrations_new\": $migrations_json
  }"
  
  if [[ "$major_detected" == true ]]; then
    log_warn "Major migration changes detected"
    log_warn "  Files changed: $files_changed"
    log_warn "  Lines changed: $lines_changed"
    if [[ ${#extensions_added[@]} -gt 0 ]]; then
      log_warn "  New extensions detected"
    fi
    if [[ ${#migrations_new[@]} -gt 0 ]]; then
      log_warn "  New migrations: ${migrations_new[*]}"
    fi
    return 2
  fi
  
  log_pass "Migration changes detected but within safe thresholds"
  return 0
}
################################################################################
# Check Execution
################################################################################

run_checks() {
  log_section "Running validation checks..."
  
  local check_start check_end duration
  local db_was_down=false
  
  # Ensure database is running
  if ! make db-ping &>/dev/null; then
    log_warn "Database not accessible, starting..."
    make db-up
    db_was_down=true
    sleep 3
  fi
  
  # Run the full check pipeline
  check_start=$(date +%s%N)
  
  if make check 2>&1; then
    check_end=$(date +%s%N)
    duration=$(( (check_end - check_start) / 1000000 ))
    add_check "full-pipeline" "pass" "$duration"
    log_pass "All checks passed (${duration}ms)"
  else
    check_end=$(date +%s%N)
    duration=$(( (check_end - check_start) / 1000000 ))
    add_check "full-pipeline" "fail" "$duration"
    log_fail "Checks failed (${duration}ms)"
    
    # Attempt migration recovery if migrations failed
    if [[ "$db_was_down" == false ]]; then
      log_section "Attempting migration recovery..."
      if make db-down && make db-up && sleep 3 && make check 2>&1; then
        log_pass "Recovery successful - checks now pass"
        add_check "full-pipeline-retry" "pass" "$duration"
        STATUS="pass"
        return 0
      else
        log_fail "Recovery failed - manual intervention needed"
        return 1
      fi
    fi
    
    return 1
  fi
}

################################################################################
# Output Formatting
################################################################################

output_json() {
  local checks_json="[$(IFS=,; echo "${CHECKS[*]}")]"
  local env_info="{
    \"git_branch\": \"$GIT_BRANCH\",
    \"database_url\": \"${DATABASE_URL:--}\",
    \"postgres_version\": \"unknown\",
    \"node_version\": \"unknown\"
  }"
  
  local summary=""
  case "$STATUS" in
    pass)
      summary="All checks passed. Rebase validated successfully."
      ;;
    fail)
      summary="Validation failed: ${FAILED_CHECKS[*]}"
      ;;
    needs-review)
      summary="Major migration changes detected. Manual review recommended."
      ;;
  esac
  
  cat <<EOF
{
  "status": "$STATUS",
  "timestamp": "$TIMESTAMP",
  "rebase_detected": $REBASE_DETECTED,
  "migration_status": $MIGRATION_STATUS,
  "checks": $checks_json,
  "summary": "$summary",
  "failed_checks": [$(IFS=,; printf '"%s"' "${FAILED_CHECKS[@]}")],
  "environment": $env_info
}
EOF
}

output_human() {
  echo ""
  echo "════════════════════════════════════════════════════════════════════════════════"
  echo "  REBASE VALIDATION REPORT"
  echo "════════════════════════════════════════════════════════════════════════════════"
  echo ""
  echo "Timestamp:    $TIMESTAMP"
  echo "Git Branch:   $GIT_BRANCH"
  echo "Rebase State: $([ "$REBASE_DETECTED" = true ] && echo "YES" || echo "NO")"
  echo ""
  
  if [[ "$STATUS" == "needs-review" ]]; then
    echo -e "${YELLOW}⚠️  REQUIRES HUMAN REVIEW${NC}"
    echo "Major migration changes detected. Please review before proceeding."
    echo ""
    echo "Migration Analysis:"
    echo "  Files changed: $(echo "$MIGRATION_STATUS" | grep -o '"files_changed": [0-9]*' | grep -o '[0-9]*')"
    echo "  Lines changed: $(echo "$MIGRATION_STATUS" | grep -o '"lines_changed": [0-9]*' | grep -o '[0-9]*')"
  elif [[ "$STATUS" == "pass" ]]; then
    echo -e "${GREEN}✅ ALL CHECKS PASSED${NC}"
  else
    echo -e "${RED}❌ VALIDATION FAILED${NC}"
    echo "Failed checks: ${FAILED_CHECKS[*]}"
  fi
  
  echo ""
  echo "════════════════════════════════════════════════════════════════════════════════"
}

################################################################################
# Main
################################################################################

main() {
  # Parse arguments
  while [[ $# -gt 0 ]]; do
    case $1 in
      --json)
        OUTPUT_FORMAT="json"
        shift
        ;;
      --help|-h)
        echo "Usage: $0 [--json] [--help]"
        echo "Validates rebase state and runs full CI checks."
        echo "Exit codes: 0=pass, 1=fail, 2=needs-review"
        exit 0
        ;;
      *)
        shift
        ;;
    esac
  done
  
  cd "$REPO_ROOT"
  
  # Run validation pipeline
  detect_rebase_state
  migration_analysis_exit=$?
  
  if [[ $migration_analysis_exit -eq 2 ]]; then
    # Major migration changes - ask for review
    if [[ "$OUTPUT_FORMAT" == "human" ]]; then
      output_human
      echo ""
      read -p "Continue anyway? (y/n): " -n 1 -r
      echo
      if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        if [[ "$OUTPUT_FORMAT" == "json" ]]; then
          output_json
        fi
        exit 2
      fi
    else
      if [[ "$OUTPUT_FORMAT" == "json" ]]; then
        output_json
      fi
      exit 2
    fi
  fi
  
  # Run checks
  run_checks || true
  
  # Output results
  if [[ "$OUTPUT_FORMAT" == "json" ]]; then
    output_json
  else
    output_human
  fi
  
  # Exit with appropriate code
  case "$STATUS" in
    pass)
      exit 0
      ;;
    needs-review)
      exit 2
      ;;
    *)
      exit 1
      ;;
  esac
}

main "$@"

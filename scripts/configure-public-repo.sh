#!/bin/sh
# One-time setup after making the repository public. No visibility changes.
set -eu

repo=Hinkolas/skali
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
case "${1:-}" in
  --apply) ;;
  "")
    printf '%s\n' "Public-repository setup for $repo:" \
      "Enable private vulnerability reporting, Dependabot alerts, secret scanning and push protection." \
      "Protect main with the following policy (zero approvals supports a solo maintainer):"
    cat "$root/.github/main-protection.json"
    printf '\n%s\n' "Run $0 --apply after publication. Existing branch protection is never replaced."
    exit 0 ;;
  *) printf 'usage: %s [--apply]\n' "$0" >&2; exit 2 ;;
esac

visibility=$(gh api "repos/$repo" --jq .visibility)
if [ "$visibility" != public ]; then
  printf '%s\n' "Repository is $visibility; public-only setup was not applied." >&2
  exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
# Do not weaken an existing policy, and do not mistake a permission failure
# for an unprotected branch.
if gh api "repos/$repo/branches/main/protection" >"$work/protection.json" 2>"$work/error"; then
  printf '%s\n' 'main already has protection; review it against .github/main-protection.json before proceeding.' >&2
  exit 1
elif ! grep -q '(HTTP 404)' "$work/error"; then
  cat "$work/error" >&2
  exit 1
fi

gh api --method PUT "repos/$repo/private-vulnerability-reporting"
gh api --method PUT "repos/$repo/vulnerability-alerts"
gh api --method PATCH "repos/$repo" --input - --silent <<'JSON'
{"security_and_analysis":{"secret_scanning":{"status":"enabled"},"secret_scanning_push_protection":{"status":"enabled"}}}
JSON
gh api --method PUT "repos/$repo/branches/main/protection" \
  --input "$root/.github/main-protection.json" --silent

gh api "repos/$repo/private-vulnerability-reporting"
gh api "repos/$repo/vulnerability-alerts" --silent
gh api "repos/$repo" --jq .security_and_analysis
gh api "repos/$repo/branches/main/protection" \
  --jq '{required_status_checks, enforce_admins, required_pull_request_reviews, allow_force_pushes, allow_deletions}'

#!/usr/bin/env bash
# Publish a VSIX to the VS Code Marketplace (#460).
#
#   .github/scripts/vsce-publish.sh <vsix> [vsce publish flags, e.g. --pre-release]
#
# Trusted Publishing first: GitHub's OIDC token is exchanged for a 15-minute
# Marketplace credential, with no stored secret. It needs the job's
# `permissions: id-token: write` and a Trusted Publishing policy on the
# joyauto publisher naming this repo and the calling workflow's file
# (publish.yml, vscode-stable.yml). See RELEASING.md.
#
# If that fails (no policy yet, the exchange refused), the VSCE_PAT secret
# publishes instead, with a warning, until the PAT is retired. With neither,
# the step fails here, and is skipped in a fork (no policy, no secret), as it
# always was.
set -uo pipefail
vsix=${1:?usage: vsce-publish.sh <vsix> [flags]}; shift
vsce() { npx --yes @vscode/vsce@4.1.0 publish "$@" --skip-duplicate --packagePath "$vsix"; }

if [ -n "${ACTIONS_ID_TOKEN_REQUEST_URL:-}" ]; then
  if vsce "$@" --oidc; then
    echo "published to the Marketplace with Trusted Publishing (OIDC)"
    exit 0
  fi
  echo "::warning::Marketplace Trusted Publishing (vsce --oidc) failed; falling back to VSCE_PAT. Check the joyauto publisher's Trusted Publishing policy for ${GITHUB_WORKFLOW_REF:-this workflow} (#460)."
else
  echo "::warning::no OIDC token in this job (permissions: id-token: write missing); using VSCE_PAT"
fi
if [ -z "${VSCE_PAT:-}" ]; then
  if [ "${GITHUB_REPOSITORY:-}" != joyautomation/nautilus ]; then
    echo "not joyautomation/nautilus and VSCE_PAT not set — skipping Marketplace publish"
    exit 0
  fi
  echo "::error::the Marketplace publish failed: Trusted Publishing did not publish and VSCE_PAT is not set (#460)"
  exit 1
fi
vsce "$@" -p "$VSCE_PAT"

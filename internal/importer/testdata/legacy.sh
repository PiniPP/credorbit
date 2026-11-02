#!/bin/bash
# Synthetic fixture shaped like the legacy populate-aws-creds.sh. All names are fake.
#   ./legacy.sh main   — only main accounts

CREDS_FILE="${CREDS_FILE:-$HOME/.aws/credentials}"
PARALLEL="${PARALLEL:-7}"

# TLS-intercepting network detection.
SASE_CA="${SASE_CA:-$HOME/.aws/ExampleRootCA.pem}"
if [ -z "$AWS_CA_BUNDLE" ] && [ "$SASE_CA" != "none" ] && [ -f "$SASE_CA" ]; then
  if curl -s -o /dev/null --max-time 5 --cacert "$SASE_CA" https://portal.sso.example-region.amazonaws.com/; then
    export AWS_CA_BUNDLE="$SASE_CA"
  fi
fi

SSO_SESSION="${SSO_SESSION:-example}"
if ! aws sts get-caller-identity --profile p_alpha-dev >/dev/null 2>&1; then
  aws sso login --sso-session "$SSO_SESSION" || exit 1
fi

MAIN_PROFILES=(
  # team alpha
  p_alpha-dev
  p_alpha-prd   # trailing comment
  "p_beta-stg"
  # p_commented-out
  #p_also-commented

  p_gamma-sandbox-01 p_gamma-sandbox-02
  p_alpha-dev
  p_not-configured
)

removed_profiles=($(awk '$1=="removed"{print $2}' "$SYNC_OUT"))

_keep=()
for m in "${MAIN_PROFILES[@]}"; do
  _keep+=("$m")
done
MAIN_PROFILES=("${_keep[@]}")

FALLBACK_ROLES="${FALLBACK_ROLES:-AdministratorAccess Example-Admin-Limited}"
export FALLBACK_ROLES

export_section() {
  local profile="$1" name="$2" out="$3"
  echo "$profile $name $out"
}

for profile in "${profiles[@]}"; do
  export_section "$profile" "${profile#p_}" "$workdir/$profile.ini"
done

if export_section p_alpha-dev default "$workdir/default.ini"; then
  cat "$workdir/default.ini" > "$new"
fi

CLAUDE_ROTATION=(
  gamma-sandbox-01
  gamma-sandbox-02
  alpha-dev
)
_rot=()
for a in "${CLAUDE_ROTATION[@]}"; do grep -q "^\[$a\]" "$CREDS_FILE" && _rot+=("$a"); done
[ ${#_rot[@]} -gt 0 ] && CLAUDE_ROTATION=("${_rot[@]}")
CLAUDE_ACCT_FILE="$HOME/.claude-aws-account"

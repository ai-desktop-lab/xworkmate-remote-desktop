#!/usr/bin/env bash
set -euo pipefail

PRODUCT="${1:?product required}"
TAG="${2:?tag required}"
DIST_DIR="${3:?dist dir required}"
CHANNEL="${4:-latest}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
python3 "${SCRIPT_DIR}/build_release_matrix.py" \
  --product "$PRODUCT" \
  --tag "$TAG" \
  --channel "$CHANNEL" \
  --dist-dir "$DIST_DIR" \
  --output "${DIST_DIR}/release-matrix.json"

if [[ -n "${R2_ACCESS_KEY_ID:-}" && -n "${R2_SECRET_ACCESS_KEY:-}" && -n "${R2_ENDPOINT:-}" ]]; then
  R2_BUCKET="${R2_BUCKET:-xconnect-dl-global}"
  AWS_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID" \
  AWS_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY" \
  AWS_DEFAULT_REGION="auto" \
  aws s3 sync "$DIST_DIR" "s3://${R2_BUCKET}/releases/${PRODUCT}/${TAG}/" \
    --endpoint-url "$R2_ENDPOINT" \
    --exclude "release-matrix.json" --no-progress
  AWS_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID" \
  AWS_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY" \
  AWS_DEFAULT_REGION="auto" \
  aws s3 sync "$DIST_DIR" "s3://${R2_BUCKET}/releases/${PRODUCT}/latest/" \
    --endpoint-url "$R2_ENDPOINT" \
    --exclude "release-matrix.json" --no-progress
  AWS_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID" \
  AWS_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY" \
  AWS_DEFAULT_REGION="auto" \
  aws s3 cp "${DIST_DIR}/release-matrix.json" "s3://${R2_BUCKET}/releases/${PRODUCT}/release-matrix.json" \
    --endpoint-url "$R2_ENDPOINT" \
    --content-type "application/json" --cache-control "max-age=60"
else
  echo "Skipping R2 sync (credentials not loaded)"
fi

if [[ -n "${OSS_ACCESS_KEY_ID:-}" && -n "${OSS_ACCESS_KEY_SECRET:-}" && -n "${OSS_ENDPOINT:-}" ]]; then
  OSS_BUCKET="${OSS_BUCKET:-xconnect-dl-cn-hk}"
  if ! command -v ossutil >/dev/null 2>&1; then
    curl -fsSL https://gosspublic.alicdn.com/ossutil/1.7.19/ossutil64 -o /usr/local/bin/ossutil
    chmod +x /usr/local/bin/ossutil
  fi
  ossutil config -e "$OSS_ENDPOINT" -i "$OSS_ACCESS_KEY_ID" -k "$OSS_ACCESS_KEY_SECRET" -L EN >/dev/null
  ossutil cp -rf "$DIST_DIR" "oss://${OSS_BUCKET}/releases/${PRODUCT}/${TAG}/" --exclude "release-matrix.json"
  ossutil cp -rf "$DIST_DIR" "oss://${OSS_BUCKET}/releases/${PRODUCT}/latest/" --exclude "release-matrix.json"
  ossutil cp -f "${DIST_DIR}/release-matrix.json" "oss://${OSS_BUCKET}/releases/${PRODUCT}/release-matrix.json" \
    --meta "Content-Type:application/json#Cache-Control:max-age=60"
else
  echo "Skipping OSS sync (credentials not loaded)"
fi

if [[ -n "${DISPATCH_TOKEN:-}" ]]; then
  curl -fsSL -X POST \
    -H "Accept: application/vnd.github+json" \
    -H "Authorization: Bearer ${DISPATCH_TOKEN}" \
    https://api.github.com/repos/ai-workspace-lab/distribution/dispatches \
    -d "{\"event_type\":\"product-release\",\"client_payload\":{\"product\":\"${PRODUCT}\",\"tag\":\"${TAG}\",\"channel\":\"${CHANNEL}\"}}"
else
  echo "Skipping distribution dispatch (DISPATCH_TOKEN not set)"
fi

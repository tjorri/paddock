#!/usr/bin/env bash
#
# Refresh the digest pinned in internal/cli/logs.go::defaultReaderImage.
# Run when busybox publishes a new build under tag 1.37 (rare).
# Not gated by CI — manual operator action.
#
# Resolves the digest of busybox:1.37 via crane (preferred) or
# docker buildx imagetools (fallback) and rewrites the constant in
# internal/cli/logs.go.

set -euo pipefail

REF="busybox:1.37"
TARGET="internal/cli/logs.go"

resolve_digest() {
    if command -v crane >/dev/null 2>&1; then
        crane digest "$REF"
    elif command -v docker >/dev/null 2>&1 && docker buildx version >/dev/null 2>&1; then
        # docker buildx 0.24 ignores simple field templates like
        # `{{.Manifest.Digest}}` and emits the full inspect view, so
        # parse the top-level `Digest:` line out of the structured
        # output. The first such line is always the manifest list /
        # OCI image index digest.
        docker buildx imagetools inspect "$REF" 2>/dev/null \
            | awk '/^Digest:[[:space:]]/ { print $2; exit }'
    else
        echo "ERROR: need 'crane' or 'docker buildx' to resolve digest for $REF" >&2
        echo "Install crane: go install github.com/google/go-containerregistry/cmd/crane@latest" >&2
        exit 1
    fi
}

DIGEST="$(resolve_digest)"

case "$DIGEST" in
    sha256:*) ;;
    *) echo "ERROR: unexpected digest format: $DIGEST (expected 'sha256:<hex>')" >&2; exit 1 ;;
esac

echo "Updating $TARGET defaultReaderImage to $REF@$DIGEST"

# Match exactly the busybox:1.37@sha256:<hex> pattern so we don't
# accidentally rewrite anything else in the file.
sed -i.bak -E "s|busybox:1\.37@sha256:[a-f0-9]+|${REF}@${DIGEST}|" "$TARGET"
rm -f "$TARGET.bak"

echo "Done. Run 'go test ./internal/cli/...' to verify the constant test passes."

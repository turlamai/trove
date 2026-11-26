#!/bin/bash
# Trove full build: Go daemon (linux/amd64), frontend bundle, Slackware
# package (.txz), rendered .plg with SHA-256. Clean checkout → reproducible
# artifacts; no private steps (specification §9).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

VERSION="${1:-}"
if [ -z "$VERSION" ]; then
  echo "usage: build.sh <version>   (e.g. build.sh 0.1.0)" >&2
  exit 1
fi

DIST="$ROOT/dist"
PKGROOT="$DIST/pkgroot"
PKGNAME="trove-${VERSION}-x86_64-1"
rm -rf "$DIST"
mkdir -p "$PKGROOT"

echo "== go vet + tests (portable) =="
( cd daemon && go vet ./... )

echo "== go build (linux/amd64) =="
( cd daemon && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o "$PKGROOT/usr/local/sbin/troved" ./cmd/troved )

echo "== frontend build =="
( cd frontend && npm ci --no-fund --no-audit && npm run build )
WEB="$PKGROOT/usr/local/emhttp/plugins/trove/web"
mkdir -p "$WEB"
cp frontend/dist/app.js frontend/dist/app.css "$WEB/"
[ -f frontend/dist/index.html ] && cp frontend/dist/index.html "$WEB/" || true

echo "== plugin tree =="
PLGROOT="$PKGROOT/usr/local/emhttp/plugins/trove"
mkdir -p "$PLGROOT"
cp -R plugin/root/usr/local/emhttp/plugins/trove/. "$PLGROOT/"
chmod 755 "$PLGROOT/scripts/start-daemon.sh" \
          "$PLGROOT/event/started/trove-start" \
          "$PLGROOT/event/stopping/trove-stop" \
          "$PKGROOT/usr/local/sbin/troved"

# Slackware package metadata + post-install script
mkdir -p "$PKGROOT/install"
cat > "$PKGROOT/install/doinst.sh" <<'EOS'
#!/bin/bash
# fix ownership/perms on the daemon binary and hook scripts
chown root:root usr/local/sbin/troved 2>/dev/null || true
chmod 755 usr/local/sbin/troved 2>/dev/null || true
mkdir -p run/trove 2>/dev/null || true
EOS
cat > "$PKGROOT/install/slack-desc" <<'EOS'
trove: Trove — native file manager plugin for Unraid
trove:
trove: Storage-aware file manager for the Unraid WebGUI: shares-first
trove: browsing, dual-pane workflow, background jobs with staging →
trove: verify → atomic commit, and hard refusal of user-share/disk
trove: namespace mixing.
trove:
trove: https://github.com/turlamai/trove
trove:
trove:
trove:
trove:
EOS

echo "== package (.txz) =="
# GNU tar (CI/release) preserves root ownership; bsdtar (macOS dev) degrades
# gracefully — release artifacts are always built on Linux CI.
if tar --version 2>/dev/null | grep -q GNU; then
  ( cd "$PKGROOT" && tar -cJf "$DIST/${PKGNAME}.txz" \
      --owner=0 --group=0 --numeric-owner \
      usr install )
else
  echo "note: non-GNU tar; package ownership not normalized (CI builds releases)"
  ( cd "$PKGROOT" && COPYFILE_DISABLE=1 tar -cJf "$DIST/${PKGNAME}.txz" usr install )
fi

echo "== render .plg =="
SHA=$(sha256sum "$DIST/${PKGNAME}.txz" 2>/dev/null | awk '{print $1}')
if [ -z "${SHA:-}" ]; then
  SHA=$(shasum -a 256 "$DIST/${PKGNAME}.txz" | awk '{print $1}')
fi
sed -e "s/__TROVE_VERSION__/${VERSION}/" \
    -e "s/__TROVE_SHA256__/${SHA}/" \
    plugin/trove.plg > "$DIST/trove.plg"

echo "== checksums =="
( cd "$DIST" && { sha256sum "${PKGNAME}.txz" trove.plg 2>/dev/null || shasum -a 256 "${PKGNAME}.txz" trove.plg; } > SHA256SUMS )

echo "build complete:"
ls -la "$DIST"

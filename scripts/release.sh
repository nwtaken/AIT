#!/usr/bin/env bash
# Publish a new AIT version. Every installed AIT will then offer the update.
#
#   scripts/release.sh 1.0.1 "Fixed the model switcher on small windows."
#
# Needs: Go, wails, NSIS (makensis) and the GitHub CLI (gh, logged in).
set -euo pipefail

V="${1:?usage: scripts/release.sh <version> [notes]}"
NOTES="${2:-AIT $V}"
[[ "$V" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "version must look like 1.2.3"; exit 1; }

cd "$(dirname "$0")/.."
TC="${USERPROFILE:-$HOME}/toolchains"
export PATH="$TC/go/bin:$PATH:${USERPROFILE:-$HOME}/go/bin:$TC/nsis-3.10:$TC/gh/bin"

if [[ -n "$(git status --porcelain)" ]]; then echo "commit or stash your changes first"; exit 1; fi
if git rev-parse "v$V" >/dev/null 2>&1; then echo "v$V already exists"; exit 1; fi

# The version shown in the installer and Add/Remove Programs.
python - "$V" <<'PY'
import json, sys
c = json.load(open("wails.json", encoding="utf-8"))
c["info"]["productVersion"] = sys.argv[1]
json.dump(c, open("wails.json", "w", encoding="utf-8"), indent=2)
PY

go test ./...
wails build -s -trimpath -nsis -ldflags "-X main.Version=$V"

mkdir -p dist
cp build/bin/AIT-amd64-installer.exe dist/AIT-setup.exe
sha256sum dist/AIT-setup.exe | cut -d' ' -f1 > dist/AIT-setup.exe.sha256
echo "sha256: $(cat dist/AIT-setup.exe.sha256)"

git add wails.json
git diff --cached --quiet || git commit -m "Release v$V"
git tag -a "v$V" -m "AIT $V"
git push origin HEAD "v$V"
gh release create "v$V" dist/AIT-setup.exe dist/AIT-setup.exe.sha256 --title "AIT $V" --notes "$NOTES"
echo "Released AIT $V"

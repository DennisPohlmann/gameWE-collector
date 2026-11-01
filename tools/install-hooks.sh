#!/usr/bin/env bash
# Installs a git pre-push hook that runs tools/check-all.sh (the CI checks)
# before every push; skip it once with git push --no-verify. The hook is not
# versioned: it goes into this clone's hooks directory, which its worktrees
# share. A branch without tools/check-all.sh is pushed unchecked.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
hook="$(git rev-parse --git-common-dir)/hooks/pre-push"
if [ -e "$hook" ] && ! grep -q 'tools/check-all.sh' "$hook"; then
  echo "install-hooks: $hook exists and is not ours; not replaced" >&2
  exit 1
fi
mkdir -p "$(dirname "$hook")"
cat > "$hook" <<'HOOK'
#!/bin/sh
# installed by tools/install-hooks.sh
top=$(git rev-parse --show-toplevel) || exit 1
[ -x "$top/tools/check-all.sh" ] || exit 0
exec "$top/tools/check-all.sh"
HOOK
chmod +x "$hook"
echo "installed $hook"

#!/usr/bin/env bash
set -euo pipefail

major_version() {
  local version_output
  version_output="$("$1" -version 2>&1 | head -n 1)"
  sed -E 's/.*version "([0-9]+).*/\1/' <<<"$version_output"
}

if [[ -n "${JAVA_HOME:-}" ]] && [[ -x "${JAVA_HOME}/bin/java" ]]; then
  current_major="$(
    major_version "${JAVA_HOME}/bin/java" || true
  )"
  if [[ "$current_major" =~ ^[0-9]+$ ]] && (( current_major >= 21 )); then
    export PATH="${JAVA_HOME}/bin:$PATH"
    exec "$@"
  fi
fi

if command -v java >/dev/null 2>&1; then
  current_major="$(major_version java || true)"
  if [[ "$current_major" =~ ^[0-9]+$ ]] && (( current_major >= 21 )); then
    exec "$@"
  fi
fi

if command -v /usr/libexec/java_home >/dev/null 2>&1; then
  for version in 21 22 23 24; do
    if candidate_home="$(/usr/libexec/java_home -v "$version" 2>/dev/null)"; then
      export JAVA_HOME="$candidate_home"
      export PATH="$JAVA_HOME/bin:$PATH"
      exec "$@"
    fi
  done
fi

echo "Firebase emulators require JDK 21 or newer. Install JDK 21+ or set JAVA_HOME accordingly." >&2
exit 1

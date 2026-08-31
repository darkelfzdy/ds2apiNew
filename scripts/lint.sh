#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

LINT_BIN="${GOLANGCI_LINT_BIN:-golangci-lint}"
# v2.11.4 在较新 Go 工具链上会报 “could not load export data: export data version 4
# is greater than maximum supported version 2”，导致整个 gate 跑不起来；抬到已验证版本。
BOOTSTRAP_VERSION="${GOLANGCI_LINT_VERSION:-v2.13.2}"
BOOTSTRAP_BIN="${ROOT_DIR}/.tmp/golangci-lint-${BOOTSTRAP_VERSION}"

export GOCACHE="${GOCACHE:-${ROOT_DIR}/.tmp/go-build-cache}"
export GOLANGCI_LINT_CACHE="${GOLANGCI_LINT_CACHE:-${ROOT_DIR}/.tmp/golangci-lint-cache}"
mkdir -p "$GOCACHE" "$GOLANGCI_LINT_CACHE"

bootstrap_golangci_lint() {
  local version_no_v os arch artifact archive_url tmp_dir archive_ext binary_name
  version_no_v="${BOOTSTRAP_VERSION#v}"
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m | tr '[:upper:]' '[:lower:]')"
  archive_ext="tar.gz"
  binary_name="golangci-lint"

  case "$os" in
    linux|darwin) ;;
    windows|mingw*|msys*|cygwin*) os="windows"; archive_ext="zip"; binary_name="golangci-lint.exe" ;;
    *)
      echo "unsupported OS for bootstrap: ${os}" >&2
      return 1
      ;;
  esac

  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *)
      echo "unsupported architecture for bootstrap: ${arch}" >&2
      return 1
      ;;
  esac

  artifact="${os}-${arch}"
  archive_url="https://github.com/golangci/golangci-lint/releases/download/${BOOTSTRAP_VERSION}/golangci-lint-${version_no_v}-${artifact}.${archive_ext}"

  mkdir -p "${ROOT_DIR}/.tmp"
  tmp_dir="$(mktemp -d)"
  trap 'rm -rf "${tmp_dir}"' RETURN

  curl -sSfL "${archive_url}" -o "${tmp_dir}/golangci-lint.${archive_ext}"
  case "$archive_ext" in
    zip) unzip -q "${tmp_dir}/golangci-lint.${archive_ext}" -d "${tmp_dir}" ;;
    *) tar -xzf "${tmp_dir}/golangci-lint.${archive_ext}" -C "${tmp_dir}" ;;
  esac
  cp "${tmp_dir}/golangci-lint-${version_no_v}-${artifact}/${binary_name}" "${BOOTSTRAP_BIN}"
  chmod +x "${BOOTSTRAP_BIN}"

  echo "bootstrapped golangci-lint ${BOOTSTRAP_VERSION} to ${BOOTSTRAP_BIN}" >&2
}

run_lint() {
  # $1 是可执行文件路径，**可能含空格**（例如仓库放在 "/Volumes/1T 原装/..."）。
  # 旧实现对含空格路径改走 eval，反而把路径按空白拆断，把 "/Volumes/1T"
  # 当命令执行，报 "line 62: /Volumes/1T: No such file or directory"，
  # lint 根本没跑起来。正确做法是直接引用执行；需要额外参数时用
  # GOLANGCI_LINT_ARGS（那个变量才允许按空白切词）。
  local exe="$1"
  local -a extra=()
  if [[ -n "${GOLANGCI_LINT_ARGS:-}" ]]; then
    read -r -a extra <<< "${GOLANGCI_LINT_ARGS}"
  fi
  if [[ ${#extra[@]} -gt 0 ]]; then
    "${exe}" "${extra[@]}" fmt --diff -c .golangci.yml && "${exe}" "${extra[@]}" run -c .golangci.yml ./...
  else
    "${exe}" fmt --diff -c .golangci.yml && "${exe}" run -c .golangci.yml ./...
  fi
}

is_compatibility_error() {
  case "$1" in
    *"command not found"*|\
    *"not recognized as an internal or external command"*|\
    *"No such file or directory"*|\
    *"unknown command \"fmt\""*|\
    *"unknown command \"run\""*|\
    *"unknown flag"*|\
    *"no such flag"*|\
    *"unsupported version of the configuration"*|\
    *"can't load config"*|\
    *"could not load export data"*|\
    *"export data version"*)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

# v2 separates formatters from linters; enforce both in one entrypoint.
if lint_output="$(run_lint "$LINT_BIN" 2>&1)"; then
  [[ -n "$lint_output" ]] && echo "$lint_output"
  exit 0
fi

if [[ -n "${GOLANGCI_LINT_BIN:-}" ]]; then
  echo "$lint_output" >&2
  echo "lint failed with explicit GOLANGCI_LINT_BIN=${GOLANGCI_LINT_BIN}; skip auto-bootstrap." >&2
  exit 1
fi

if ! is_compatibility_error "$lint_output"; then
  echo "$lint_output" >&2
  exit 1
fi

echo "default golangci-lint appears incompatible; bootstrapping ${BOOTSTRAP_VERSION}..." >&2
if [[ ! -x "${BOOTSTRAP_BIN}" ]]; then
  bootstrap_golangci_lint
fi

if lint_output="$(run_lint "${BOOTSTRAP_BIN}" 2>&1)"; then
  [[ -n "$lint_output" ]] && echo "$lint_output"
  exit 0
fi

echo "$lint_output" >&2
exit 1

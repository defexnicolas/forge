#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$SCRIPT_DIR"
GO_VERSION="${GO_VERSION:-$(sed -n 's/^go //p' "$REPO_DIR/go.mod" | head -n1)}"
FORGETUI_HOME="${FORGETUI_HOME:-$HOME/.forgetui}"
TOOLS_DIR="$FORGETUI_HOME/tools"
TMP_DIR="${TMPDIR:-$FORGETUI_HOME/tmp}"
GO_DOWNLOAD_BASE="${GO_DOWNLOAD_BASE:-https://go.dev/dl}"
INSTALL_OPTIONAL_RUNTIMES="${INSTALL_OPTIONAL_RUNTIMES:-1}"
INSTALL_TO_USER_BIN="${INSTALL_TO_USER_BIN:-1}"

PLATFORM=""
ARCH=""
GO_ARCHIVE_EXT=""
FORGE_OUTPUT=""
GO_BIN=""

log() {
  printf '[forgetui] %s\n' "$*"
}

warn() {
  printf '[forgetui] warning: %s\n' "$*" >&2
}

die() {
  printf '[forgetui] error: %s\n' "$*" >&2
  exit 1
}

have() {
  command -v "$1" >/dev/null 2>&1
}

run_privileged() {
  if have sudo; then
    sudo "$@"
    return
  fi
  "$@"
}

to_native_path() {
  if [ "$PLATFORM" = "windows" ] && have cygpath; then
    cygpath -w "$1"
    return
  fi
  printf '%s' "$1"
}

powershell_cmd() {
  if ! have powershell.exe; then
    die "powershell.exe no esta disponible; en Windows ejecuta este script desde Git Bash o instala PowerShell"
  fi
  powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "$1"
}

first_cmd() {
  local candidate
  for candidate in "$@"; do
    if have "$candidate"; then
      printf '%s' "$candidate"
      return 0
    fi
  done
  return 1
}

detect_platform() {
  local uname_s uname_m
  uname_s="$(uname -s | tr '[:upper:]' '[:lower:]')"
  uname_m="$(uname -m | tr '[:upper:]' '[:lower:]')"

  case "$uname_s" in
    linux*)
      PLATFORM="linux"
      GO_ARCHIVE_EXT="tar.gz"
      FORGE_OUTPUT="$REPO_DIR/forge"
      ;;
    darwin*)
      PLATFORM="darwin"
      GO_ARCHIVE_EXT="tar.gz"
      FORGE_OUTPUT="$REPO_DIR/forge"
      ;;
    msys*|mingw*|cygwin*)
      PLATFORM="windows"
      GO_ARCHIVE_EXT="zip"
      FORGE_OUTPUT="$REPO_DIR/forge.exe"
      ;;
    *)
      die "plataforma no soportada por este bootstrap: $(uname -s)"
      ;;
  esac

  case "$uname_m" in
    x86_64|amd64)
      ARCH="amd64"
      ;;
    arm64|aarch64)
      ARCH="arm64"
      ;;
    *)
      die "arquitectura no soportada por este bootstrap: $(uname -m)"
      ;;
  esac
}

download_file() {
  local url="$1"
  local out="$2"

  if have curl; then
    curl -fsSL "$url" -o "$out"
    return
  fi
  if have wget; then
    wget -qO "$out" "$url"
    return
  fi
  if [ "$PLATFORM" = "windows" ]; then
    powershell_cmd "\$ProgressPreference='SilentlyContinue'; Invoke-WebRequest -Uri '$url' -OutFile '$(to_native_path "$out")'"
    return
  fi
  die "no encontre curl ni wget para descargar $url"
}

linux_package_manager() {
  if have apt-get; then
    printf 'apt'
    return
  fi
  if have dnf; then
    printf 'dnf'
    return
  fi
  if have yum; then
    printf 'yum'
    return
  fi
  if have pacman; then
    printf 'pacman'
    return
  fi
  if have zypper; then
    printf 'zypper'
    return
  fi
  if have apk; then
    printf 'apk'
    return
  fi
  printf ''
}

install_linux_group() {
  local group="$1"
  local pm
  pm="$(linux_package_manager)"
  [ -n "$pm" ] || die "no encontre package manager soportado para Linux"

  case "$pm" in
    apt)
      case "$group" in
        git) run_privileged apt-get update && run_privileged apt-get install -y git ca-certificates ;;
        python) run_privileged apt-get update && run_privileged apt-get install -y python3 python3-pip python3-venv ;;
        node) run_privileged apt-get update && run_privileged apt-get install -y nodejs npm ;;
        *) die "grupo Linux desconocido: $group" ;;
      esac
      ;;
    dnf)
      case "$group" in
        git) run_privileged dnf install -y git ca-certificates ;;
        python) run_privileged dnf install -y python3 python3-pip ;;
        node) run_privileged dnf install -y nodejs npm ;;
        *) die "grupo Linux desconocido: $group" ;;
      esac
      ;;
    yum)
      case "$group" in
        git) run_privileged yum install -y git ca-certificates ;;
        python) run_privileged yum install -y python3 python3-pip ;;
        node) run_privileged yum install -y nodejs npm ;;
        *) die "grupo Linux desconocido: $group" ;;
      esac
      ;;
    pacman)
      case "$group" in
        git) run_privileged pacman -Sy --noconfirm git ca-certificates ;;
        python) run_privileged pacman -Sy --noconfirm python python-pip ;;
        node) run_privileged pacman -Sy --noconfirm nodejs npm ;;
        *) die "grupo Linux desconocido: $group" ;;
      esac
      ;;
    zypper)
      case "$group" in
        git) run_privileged zypper --non-interactive install git ca-certificates ;;
        python) run_privileged zypper --non-interactive install python3 python3-pip ;;
        node) run_privileged zypper --non-interactive install nodejs npm ;;
        *) die "grupo Linux desconocido: $group" ;;
      esac
      ;;
    apk)
      case "$group" in
        git) run_privileged apk add --no-cache git ca-certificates ;;
        python) run_privileged apk add --no-cache python3 py3-pip ;;
        node) run_privileged apk add --no-cache nodejs npm ;;
        *) die "grupo Linux desconocido: $group" ;;
      esac
      ;;
  esac
}

install_macos_group() {
  local group="$1"
  if ! have brew; then
    warn "Homebrew no esta instalado; no puedo instalar automaticamente $group en macOS"
    return 1
  fi

  case "$group" in
    git) brew install git ;;
    python) brew install python ;;
    node) brew install node ;;
    *) die "grupo macOS desconocido: $group" ;;
  esac
}

install_windows_group() {
  local group="$1"
  local winget_cmd choco_cmd
  winget_cmd="$(first_cmd winget winget.exe || true)"
  choco_cmd="$(first_cmd choco choco.exe || true)"

  if [ -n "$winget_cmd" ]; then
    case "$group" in
      git) "$winget_cmd" install -e --id Git.Git --accept-package-agreements --accept-source-agreements ;;
      python) "$winget_cmd" install -e --id Python.Python.3.12 --accept-package-agreements --accept-source-agreements ;;
      node) "$winget_cmd" install -e --id OpenJS.NodeJS.LTS --accept-package-agreements --accept-source-agreements ;;
      *) die "grupo Windows desconocido: $group" ;;
    esac
    return
  fi
  if [ -n "$choco_cmd" ]; then
    case "$group" in
      git) "$choco_cmd" install -y git ;;
      python) "$choco_cmd" install -y python ;;
      node) "$choco_cmd" install -y nodejs-lts ;;
      *) die "grupo Windows desconocido: $group" ;;
    esac
    return
  fi
  if have scoop; then
    case "$group" in
      git) scoop install git ;;
      python) scoop install python ;;
      node) scoop install nodejs-lts ;;
      *) die "grupo Windows desconocido: $group" ;;
    esac
    return
  fi

  warn "no encontre winget, choco ni scoop; no puedo instalar automaticamente $group en Windows"
  return 1
}

install_system_group() {
  local group="$1"
  case "$PLATFORM" in
    linux) install_linux_group "$group" ;;
    darwin) install_macos_group "$group" ;;
    windows) install_windows_group "$group" ;;
    *) die "plataforma invalida: $PLATFORM" ;;
  esac
}

ensure_git() {
  if have git; then
    return
  fi
  log "git no esta instalado; intentando instalarlo"
  install_system_group git || die "git es obligatorio para descargar dependencias de Go"
  have git || die "git sigue sin estar disponible despues de la instalacion"
}

install_private_go() {
  local version_dir archive_name archive_path url parent_dir go_root
  version_dir="$TOOLS_DIR/go/$GO_VERSION"
  go_root="$version_dir/go"

  if [ -x "$go_root/bin/go" ] || [ -x "$go_root/bin/go.exe" ]; then
    log "Go $GO_VERSION ya existe en $go_root"
  else
    archive_name="go${GO_VERSION}.${PLATFORM}-${ARCH}.${GO_ARCHIVE_EXT}"
    archive_path="$TMP_DIR/$archive_name"
    url="$GO_DOWNLOAD_BASE/$archive_name"

    mkdir -p "$TMP_DIR" "$version_dir"
    log "descargando Go $GO_VERSION desde $url"
    download_file "$url" "$archive_path"

    rm -rf "$go_root"
    parent_dir="$(dirname "$go_root")"
    mkdir -p "$parent_dir"

    if [ "$PLATFORM" = "windows" ]; then
      powershell_cmd "\$dst='$(to_native_path "$parent_dir")'; if (-not (Test-Path \$dst)) { New-Item -ItemType Directory -Path \$dst | Out-Null }; Expand-Archive -Path '$(to_native_path "$archive_path")' -DestinationPath \$dst -Force"
    else
      tar -C "$parent_dir" -xzf "$archive_path"
    fi
  fi

  if [ "$PLATFORM" = "windows" ]; then
    GO_BIN="$go_root/bin/go.exe"
  else
    GO_BIN="$go_root/bin/go"
  fi
  [ -x "$GO_BIN" ] || die "no pude dejar Go listo en $GO_BIN"

  export GOROOT="$go_root"
  export PATH="$go_root/bin:$PATH"
  log "usando $("$GO_BIN" version)"
}

ensure_python_best_effort() {
  if have python3 || have python; then
    return
  fi
  log "Python no esta instalado; intentando instalarlo"
  if ! install_system_group python; then
    warn "no pude instalar Python automaticamente; Forge igual puede correr, pero python_setup/python_run no estaran listos"
    return
  fi
  if have python3 || have python; then
    log "Python disponible"
  else
    warn "la instalacion de Python termino pero no aparece en PATH todavia; puede requerir reabrir la terminal"
  fi
}

ensure_node_best_effort() {
  if have node && (have npm || have npx); then
    return
  fi
  log "Node.js no esta instalado; intentando instalarlo"
  if ! install_system_group node; then
    warn "no pude instalar Node.js automaticamente; Forge igual puede correr, pero skills/npx y algunos MCPs pueden faltar"
    return
  fi
  if have node && (have npm || have npx); then
    log "Node.js disponible"
  else
    warn "la instalacion de Node.js termino pero no aparece en PATH todavia; puede requerir reabrir la terminal"
  fi
}

user_bin_dir() {
  if [ "$PLATFORM" = "windows" ]; then
    printf '%s' "${INSTALL_BIN_DIR:-$HOME/bin}"
    return
  fi
  printf '%s' "${INSTALL_BIN_DIR:-$HOME/.local/bin}"
}

install_forge_binary() {
  local target_dir target_bin
  target_dir="$(user_bin_dir)"
  mkdir -p "$target_dir"

  if [ "$PLATFORM" = "windows" ]; then
    target_bin="$target_dir/forge.exe"
  else
    target_bin="$target_dir/forge"
  fi

  cp "$FORGE_OUTPUT" "$target_bin"
  if [ "$PLATFORM" != "windows" ]; then
    chmod +x "$target_bin"
  fi

  log "binary instalado en $target_bin"
  case ":$PATH:" in
    *":$target_dir:"*) ;;
    *)
      warn "$target_dir no esta en PATH en esta shell; agrega ese directorio a tu PATH para invocar forge globalmente"
      ;;
  esac
}

build_forge() {
  log "descargando modulos de Go"
  "$GO_BIN" mod download

  log "compilando Forge"
  "$GO_BIN" build -o "$FORGE_OUTPUT" ./cmd/forge
}

print_summary() {
  local python_version node_version bin_dir path_cmd target_bin
  python_version="no instalado"
  node_version="no instalado"
  bin_dir="$(user_bin_dir)"

  if [ "$PLATFORM" = "windows" ]; then
    target_bin="$bin_dir/forge.exe"
  else
    target_bin="$bin_dir/forge"
  fi

  if have python3; then
    python_version="$(python3 --version 2>&1)"
  elif have python; then
    python_version="$(python --version 2>&1)"
  fi

  if have node; then
    node_version="$(node --version 2>&1)"
  fi

  printf '\n'
  log "instalacion lista"
  printf '  repo:          %s\n' "$REPO_DIR"
  printf '  plataforma:    %s/%s\n' "$PLATFORM" "$ARCH"
  printf '  go:            %s\n' "$("$GO_BIN" version)"
  printf '  python:        %s\n' "$python_version"
  printf '  node:          %s\n' "$node_version"
  printf '  forge output:  %s\n' "$FORGE_OUTPUT"
  printf '  forge global:  %s\n' "$target_bin"
  printf '\n'
  printf 'Para correr forge desde cualquier parte, agrega este directorio al PATH:\n'
  printf '  %s\n' "$bin_dir"
  printf '\n'
  case "$PLATFORM" in
    windows)
      path_cmd="[Environment]::SetEnvironmentVariable(\"PATH\", \$env:PATH + \";$(to_native_path "$bin_dir")\", \"User\")"
      printf 'PowerShell (usuario actual):\n'
      printf '  %s\n' "$path_cmd"
      printf '\n'
      printf 'PowerShell (solo esta sesion):\n'
      printf '  $env:PATH += \";%s\"\n' "$(to_native_path "$bin_dir")"
      ;;
    darwin)
      printf 'zsh:\n'
      printf '  echo '\''export PATH="%s:$PATH"'\'' >> ~/.zshrc\n' "$bin_dir"
      printf '  source ~/.zshrc\n'
      printf '\n'
      printf 'bash:\n'
      printf '  echo '\''export PATH="%s:$PATH"'\'' >> ~/.bashrc\n' "$bin_dir"
      printf '  source ~/.bashrc\n'
      ;;
    linux)
      printf 'bash:\n'
      printf '  echo '\''export PATH="%s:$PATH"'\'' >> ~/.bashrc\n' "$bin_dir"
      printf '  source ~/.bashrc\n'
      printf '\n'
      printf 'zsh:\n'
      printf '  echo '\''export PATH="%s:$PATH"'\'' >> ~/.zshrc\n' "$bin_dir"
      printf '  source ~/.zshrc\n'
      ;;
  esac
  printf '\n'
  printf 'Despues puedes probar:\n'
  printf '  forge --help\n'
  printf '  forge\n'
  printf '\n'
  printf 'Nota: este script instala el toolchain y compila Forge. Aun necesitas un proveedor de modelos\n'
  printf '(LM Studio, OpenAI-compatible o OpenAI API) para usar el agente.\n'
}

main() {
  [ -f "$REPO_DIR/go.mod" ] || die "go.mod no existe en $REPO_DIR; ejecuta este script desde la raiz del repo"
  [ -n "$GO_VERSION" ] || die "no pude leer la version de Go desde go.mod"

  detect_platform
  mkdir -p "$TOOLS_DIR" "$TMP_DIR"

  ensure_git
  install_private_go

  if [ "$INSTALL_OPTIONAL_RUNTIMES" = "1" ]; then
    ensure_python_best_effort
    ensure_node_best_effort
  fi

  build_forge

  if [ "$INSTALL_TO_USER_BIN" = "1" ]; then
    install_forge_binary
  fi

  print_summary
}

main "$@"

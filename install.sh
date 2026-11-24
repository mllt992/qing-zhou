#!/bin/bash
# 轻舟面板一键安装 / 更新脚本
#
# 全新安装（交互式配置）与升级（保留配置、原子替换二进制）共用同一入口：
#   bash <(curl -fsSL https://raw.githubusercontent.com/mllt992/qing-zhou/main/install.sh)
#
# 可选参数：
#   --version vX.Y.Z   安装指定版本（默认取 GitHub 最新 release）
#   --force            与当前版本相同也强制重装
#   --proxy <前缀>     GitHub 下载加速前缀，如 https://mirror.ghproxy.com/
#   --init-mcp         安装并启动后自动签发管理员 MCP 令牌并输出配置 JSON
#   uninstall          卸载（保留数据库与配置，除非再确认删除）
#                      装好后本脚本会存一份到 /opt/qingzhou/install.sh，卸载直接：
#                        bash /opt/qingzhou/install.sh uninstall
#
# 非交互环境（无 TTY，如 CI）下全新安装使用默认值，可用环境变量覆盖：
#   QZ_LISTEN / QZ_PUBLIC_BASE / QZ_ADMIN_USER / QZ_ADMIN_PASS
set -euo pipefail

REPO="${QZ_REPO:-mllt992/qing-zhou}"
INSTALL_DIR="/opt/qingzhou"
BIN_PATH="$INSTALL_DIR/qingzhou"
ENV_FILE="$INSTALL_DIR/qingzhou.env"
SERVICE_NAME="qingzhou"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
VERSION_MARKER="$INSTALL_DIR/.version"

GH_PROXY="${GH_PROXY:-}"
WANT_VERSION=""
FORCE=0
INIT_MCP=0
ACTION="install"

while [ $# -gt 0 ]; do
  case "$1" in
    --version)  WANT_VERSION="$2"; shift 2 ;;
    --force)    FORCE=1; shift ;;
    --proxy)    GH_PROXY="$2"; shift 2 ;;
    --init-mcp) INIT_MCP=1; shift ;;
    uninstall)  ACTION="uninstall"; shift ;;
    *) echo "未知参数: $1"; exit 1 ;;
  esac
done

err()  { echo "❌ $*" >&2; exit 1; }
info() { echo "▸ $*"; }

# gen_hex <字节数>：生成随机十六进制串（od 读定长字节，无 SIGPIPE 之忧）
gen_hex() {
  if command -v openssl >/dev/null; then
    openssl rand -hex "$1"
  else
    od -An -N"$1" -tx1 /dev/urandom | tr -d ' \n'
  fi
}

# env_get <KEY>：从配置文件读取变量值，不存在时返回空（不触发 set -e）
env_get() { grep -E "^$1=" "$ENV_FILE" 2>/dev/null | tail -1 | cut -d= -f2- || true; }

# ---------- 交互输入：兼容 `curl | bash`（stdin 是管道时改从 /dev/tty 读） ----------
INTERACTIVE=0
if [ -t 0 ]; then
  INTERACTIVE=1
elif (: < /dev/tty) 2>/dev/null; then   # 节点存在还不够，得真能打开（容器里常常不行）
  INTERACTIVE=2
fi

# ask <提示> <默认值> [secret]  →  结果写入 REPLY_VALUE
ask() {
  local prompt="$1" def="$2" secret="${3:-}" input=""
  local shown="$prompt"
  [ -n "$def" ] && shown="$prompt [$def]"
  if [ "$INTERACTIVE" = "0" ]; then
    REPLY_VALUE="$def"
    return
  fi
  local flags=(-r)
  [ "$secret" = "secret" ] && flags+=(-s)
  if [ "$INTERACTIVE" = "2" ]; then
    read "${flags[@]}" -p "$shown: " input < /dev/tty || true
  else
    read "${flags[@]}" -p "$shown: " input || true
  fi
  [ "$secret" = "secret" ] && echo ""
  REPLY_VALUE="${input:-$def}"
}

# ---------- 环境检查 ----------
[ "$(uname -s)" = "Linux" ] || err "面板二进制仅提供 Linux 版本；其他系统请用 Docker 或源码构建"
[ "$(id -u)" = "0" ] || err "请以 root 运行（sudo bash ...）"
command -v systemctl >/dev/null || err "需要 systemd"

ARCH=$(uname -m)
case "$ARCH" in
  x86_64)          ARCH="amd64" ;;
  aarch64|arm64)   ARCH="arm64" ;;
  *) err "不支持的架构: $ARCH（仅支持 x86_64 / aarch64）" ;;
esac

HAVE_CURL=0
command -v curl >/dev/null && HAVE_CURL=1
if [ "$HAVE_CURL" = "0" ] && ! command -v wget >/dev/null; then
  err "需要 curl 或 wget"
fi

# dl <url> <dest>：下载文件，失败返回非零。有终端时显示进度条，否则静默
dl() {
  local url="$1" dest="$2"
  if [ "$HAVE_CURL" = "1" ]; then
    local progress="-sS"
    [ "$INTERACTIVE" != "0" ] && progress="--progress-bar"
    curl -fL $progress --connect-timeout 15 --retry 2 -o "$dest" "$url"
  else
    wget -qO "$dest" "$url"
  fi
}

# gh_url <path>：拼接 GitHub 下载地址（带可选加速前缀）
gh_url() { echo "${GH_PROXY}https://github.com/${REPO}/$1"; }

# save_self：把脚本自身留一份到 $INSTALL_DIR，卸载时才有东西可跑。
# curl | bash 或 bash <(curl ...) 时 $0 不是可重读的普通文件（管道已被读到 EOF），
# 这种情况回源下载一份。
save_self() {
  local dest="$INSTALL_DIR/install.sh"
  case "$0" in
    */install.sh|install.sh)
      [ -r "$0" ] && cp -f "$0" "$dest" 2>/dev/null && { chmod 755 "$dest"; return; } ;;
  esac
  dl "${GH_PROXY}https://raw.githubusercontent.com/${REPO}/main/install.sh" "$dest" 2>/dev/null \
    && chmod 755 "$dest" \
    || echo "⚠️  未能保存卸载脚本到 $dest，卸载请重跑安装命令并加 uninstall"
}

# guess_ip：取本机出网网卡地址，用于安装完打印可点的面板地址（纯本地判断，不外连）
guess_ip() { ip route get 1.1.1.1 2>/dev/null | sed -n 's/.*[[:space:]]src[[:space:]]\([0-9.]*\).*/\1/p' | head -1; }

# ---------- 卸载 ----------
if [ "$ACTION" = "uninstall" ]; then
  info "停止并移除服务..."
  systemctl disable --now "$SERVICE_NAME" 2>/dev/null || true
  rm -f "$SERVICE_FILE"
  systemctl daemon-reload
  rm -f "$BIN_PATH" "$VERSION_MARKER"
  echo "✅ 已卸载程序与服务。数据库/配置仍保留在 $INSTALL_DIR"
  ask "是否连同数据库、配置一起删除？输入 yes 确认" "no"
  if [ "$REPLY_VALUE" = "yes" ]; then
    rm -rf "$INSTALL_DIR"
    echo "✅ 已删除 $INSTALL_DIR"
  fi
  exit 0
fi

# ---------- 解析目标版本 ----------
if [ -n "$WANT_VERSION" ]; then
  TAG="$WANT_VERSION"
else
  info "获取最新版本号..."
  TAG=""
  if [ "$HAVE_CURL" = "1" ]; then
    # 跟随 releases/latest 的重定向拿 tag，不走 API、无速率限制
    EFFECTIVE=$(curl -fsSL --connect-timeout 15 -o /dev/null -w '%{url_effective}' "$(gh_url releases/latest)" || true)
    TAG="${EFFECTIVE##*/tag/}"
    case "$TAG" in http*|"") TAG="" ;; esac
  fi
  if [ -z "$TAG" ]; then
    TAG=$( (dl "https://api.github.com/repos/${REPO}/releases/latest" /dev/stdout 2>/dev/null || true) \
      | grep -o '"tag_name"[^,]*' | head -1 | sed 's/.*"\(v[^"]*\)".*/\1/' || true)
  fi
  [ -n "$TAG" ] && [ "${TAG#v}" != "$TAG" ] || err "无法获取最新版本号，可用 --version vX.Y.Z 指定，或用 --proxy 配置加速"
fi
info "目标版本: $TAG (linux-$ARCH)"

# ---------- 已装检测 ----------
CURRENT=""
if [ -x "$BIN_PATH" ]; then
  # 优先问运行中的面板（/api/health 免认证返回版本），失败退回安装标记文件
  LISTEN=$(env_get QZ_LISTEN)
  LISTEN="${LISTEN:-0.0.0.0:8081}"   # 与二进制的默认值一致（internal/config）
  HEALTH_HOST="${LISTEN/0.0.0.0/127.0.0.1}"
  if [ "$HAVE_CURL" = "1" ]; then
    CURRENT=$(curl -fsS --connect-timeout 3 "http://${HEALTH_HOST}/api/health" 2>/dev/null \
      | grep -o '"version":"[^"]*"' | cut -d'"' -f4 || true)
  fi
  [ -n "$CURRENT" ] || CURRENT=$(cat "$VERSION_MARKER" 2>/dev/null || true)
fi

MODE="install"
if [ -x "$BIN_PATH" ]; then
  MODE="update"
  if [ "$CURRENT" = "$TAG" ] && [ "$FORCE" = "0" ]; then
    echo "✅ 已是最新版本 $TAG，无需更新（--force 可强制重装）"
    exit 0
  fi
  info "检测到已安装版本: ${CURRENT:-未知} → 将更新到 $TAG（配置与数据库保持不变）"
fi

# ---------- 下载并校验 ----------
mkdir -p "$INSTALL_DIR"
TMP_BIN="${BIN_PATH}.new"
TMP_SUMS="$INSTALL_DIR/.SHA256SUMS.tmp"
trap 'rm -f "$TMP_BIN" "$TMP_SUMS"' EXIT

info "下载 qingzhou-linux-${ARCH} ..."
dl "$(gh_url "releases/download/${TAG}/qingzhou-linux-${ARCH}")" "$TMP_BIN" \
  || err "下载失败。国内网络可加 --proxy https://mirror.ghproxy.com/ 重试"
[ -s "$TMP_BIN" ] || err "下载的文件为空"

info "校验 SHA-256 ..."
if dl "$(gh_url "releases/download/${TAG}/SHA256SUMS.txt")" "$TMP_SUMS" 2>/dev/null; then
  EXPECT=$(grep -E " qingzhou-linux-${ARCH}\$" "$TMP_SUMS" | awk '{print $1}' || true)
  GOT=$(sha256sum "$TMP_BIN" | awk '{print $1}')
  [ -n "$EXPECT" ] || err "SHA256SUMS.txt 中找不到 qingzhou-linux-${ARCH}"
  [ "$EXPECT" = "$GOT" ] || err "SHA-256 校验失败（期望 $EXPECT 实际 $GOT），已中止"
else
  echo "⚠️  该 release 无 SHA256SUMS.txt，跳过校验"
fi
chmod 755 "$TMP_BIN"

# ---------- 全新安装：交互式配置 ----------
if [ "$MODE" = "install" ]; then
  echo ""
  echo "========== 轻舟面板初始配置 =========="
  [ "$INTERACTIVE" = "0" ] && echo "（无终端交互，使用默认值/环境变量）"

  # 监听地址是最容易一路回车踩坑的一项：默认 127.0.0.1 时面板只有本机连得上，
  # 没装反代的人会以为「装好了却打不开」。所以问成明确的二选一，且默认给能直连的那个。
  if [ -n "${QZ_LISTEN:-}" ]; then
    CFG_LISTEN="$QZ_LISTEN"
    info "监听地址取自环境变量 QZ_LISTEN=$CFG_LISTEN"
  else
    echo ""
    echo "面板打算怎么访问？"
    echo "  1) 直接用 IP:端口 打开        → 监听 0.0.0.0:8081（明文 HTTP，公网可达）"
    echo "  2) 前面有 nginx / caddy 反代  → 监听 127.0.0.1:8081（仅本机可连）"
    ask "输入 1 或 2，也可直接填监听地址" "1"
    case "$REPLY_VALUE" in
      1) CFG_LISTEN="0.0.0.0:8081" ;;
      2) CFG_LISTEN="127.0.0.1:8081" ;;
      *) CFG_LISTEN="$REPLY_VALUE" ;;
    esac
  fi

  ask "面板对外访问地址，如 https://node.example.com（留空=按请求 Host 自动推断，也可后续在设置页填）" "${QZ_PUBLIC_BASE:-}"
  CFG_BASE="$REPLY_VALUE"

  ask "管理员用户名" "${QZ_ADMIN_USER:-admin}"
  CFG_ADMIN="$REPLY_VALUE"

  ask "管理员密码（回车=随机生成）" "${QZ_ADMIN_PASS:-}" secret
  CFG_PASS="$REPLY_VALUE"
  PASS_GENERATED=0
  if [ -z "$CFG_PASS" ]; then
    CFG_PASS=$(gen_hex 8)
    PASS_GENERATED=1
  fi

  ask "是否由面板托管探针二进制（服务器监控用，占 ~20MB）？(Y/n)" "Y"
  CFG_PROBE=$(echo "$REPLY_VALUE" | tr '[:upper:]' '[:lower:]')

  # 加密密钥：生成后勿更换，否则已加密配置无法解密
  SECRET_KEY=$(gen_hex 32)

  PROBE_LINE="# QZ_PROBE_DIR="
  if [ "$CFG_PROBE" != "n" ] && [ "$CFG_PROBE" != "no" ]; then
    mkdir -p "$INSTALL_DIR/probe"
    for a in amd64 arm64; do
      info "下载探针 probe-linux-$a ..."
      dl "$(gh_url "releases/download/${TAG}/probe-linux-$a")" "$INSTALL_DIR/probe/probe-linux-$a" \
        || echo "⚠️  probe-linux-$a 下载失败，可稍后手动放入 $INSTALL_DIR/probe/"
    done
    chmod 755 "$INSTALL_DIR/probe/"probe-linux-* 2>/dev/null || true
    PROBE_LINE="QZ_PROBE_DIR=$INSTALL_DIR/probe"
  fi

  info "写入配置 $ENV_FILE ..."
  umask 077
  cat > "$ENV_FILE" <<EOF
# 由 install.sh 生成于 $(date '+%F %T')。参考 deploy/qingzhou.env.example
QZ_LISTEN=$CFG_LISTEN
QZ_DB=$INSTALL_DIR/qingzhou.db
$( [ -n "$CFG_BASE" ] && echo "QZ_PUBLIC_BASE=$CFG_BASE" || echo "# QZ_PUBLIC_BASE=" )
$PROBE_LINE

# 首次启动播种的管理员（登录后请尽快改密码）
QZ_ADMIN_USER=$CFG_ADMIN
QZ_ADMIN_PASS=$CFG_PASS

# 敏感配置加密密钥 —— 一旦使用请勿更换，否则已加密内容无法解密
QZ_SECRET_KEY=$SECRET_KEY
EOF
  chmod 600 "$ENV_FILE"

  info "写入 systemd 服务 ..."
  cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=QingZhou subscription panel
After=network.target

[Service]
Type=simple
WorkingDirectory=$INSTALL_DIR
EnvironmentFile=$ENV_FILE
ExecStart=$BIN_PATH
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
ProtectSystem=full
ReadWritePaths=-/etc/sing-box

[Install]
WantedBy=multi-user.target
EOF
  chmod 644 "$SERVICE_FILE"
fi

# ---------- 安装二进制并启动（原子替换：直接覆盖运行中的二进制会 ETXTBSY） ----------
mv -f "$TMP_BIN" "$BIN_PATH"
trap - EXIT
rm -f "$TMP_SUMS"
echo "$TAG" > "$VERSION_MARKER"
save_self

# 更新模式下若托管了探针，一并刷新
if [ "$MODE" = "update" ]; then
  PROBE_DIR=$(env_get QZ_PROBE_DIR)
  if [ -n "$PROBE_DIR" ] && [ -d "$PROBE_DIR" ]; then
    for a in amd64 arm64; do
      info "刷新探针 probe-linux-$a ..."
      if dl "$(gh_url "releases/download/${TAG}/probe-linux-$a")" "$PROBE_DIR/probe-linux-$a.new"; then
        chmod 755 "$PROBE_DIR/probe-linux-$a.new"
        mv -f "$PROBE_DIR/probe-linux-$a.new" "$PROBE_DIR/probe-linux-$a"
      else
        rm -f "$PROBE_DIR/probe-linux-$a.new"
        echo "⚠️  probe-linux-$a 刷新失败，保留旧版本"
      fi
    done
  fi
fi

# 升级不能顺手扩大监听范围。二进制在 QZ_LISTEN 缺失时默认 0.0.0.0:8081，而这台
# 机器上一版在同样缺失时跑的是回环 —— 直接重启就等于把一个只给本机开的面板推上
# 公网。配置里没写过的按原行为显式钉住；要直连公网是一次明确的选择，不该由升级
# 替人做主。全新安装不走这里：上面已按用户的选择写过 QZ_LISTEN。
if [ "$MODE" = "update" ] && [ -f "$ENV_FILE" ] && [ -z "$(env_get QZ_LISTEN)" ]; then
  printf 'QZ_LISTEN=127.0.0.1:8081\n' >> "$ENV_FILE"
  echo "⚠️  $ENV_FILE 里没有 QZ_LISTEN。新版在缺省时改为监听 0.0.0.0:8081（公网可达），"
  echo "    为免升级把面板暴露出去，已按原行为写入 QZ_LISTEN=127.0.0.1:8081。"
  echo "    要用 IP:端口 直连，改成 0.0.0.0:8081 再 systemctl restart $SERVICE_NAME"
fi

info "启动服务 ..."
systemctl daemon-reload
systemctl enable "$SERVICE_NAME" >/dev/null 2>&1 || true
systemctl restart "$SERVICE_NAME"

# ---------- 启动确认 ----------
sleep 2
LISTEN=$(env_get QZ_LISTEN)
LISTEN="${LISTEN:-0.0.0.0:8081}"   # 与二进制的默认值一致（internal/config）
HEALTH_HOST="${LISTEN/0.0.0.0/127.0.0.1}"
OK=0
for _ in 1 2 3 4 5; do
  if [ "$HAVE_CURL" = "1" ]; then
    curl -fsS --connect-timeout 2 "http://${HEALTH_HOST}/api/health" >/dev/null 2>&1 && { OK=1; break; }
  else
    systemctl is-active --quiet "$SERVICE_NAME" && { OK=1; break; }
  fi
  sleep 1
done

echo ""
if [ "$OK" = "1" ]; then
  if [ "$MODE" = "update" ]; then
    echo "✅ 更新完成：${CURRENT:-未知} → $TAG"
  else
    PORT="${LISTEN##*:}"
    case "$LISTEN" in
      0.0.0.0:*|:*|\[::\]:*)
        IP=$(guess_ip)
        echo "✅ 安装完成！版本 $TAG"
        echo "   面板地址: http://${IP:-<服务器IP>}:${PORT}$( [ -n "${CFG_BASE:-}" ] && echo "（对外: $CFG_BASE，需自行配置反代/证书）" )"
        if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q '^Status: active'; then
          echo "   ⚠️  ufw 已启用，若打不开先放行端口: ufw allow ${PORT}/tcp"
        elif command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
          echo "   ⚠️  firewalld 已启用，若打不开先放行端口: firewall-cmd --add-port=${PORT}/tcp --permanent && firewall-cmd --reload"
        fi
        echo "   ⚠️  当前是明文 HTTP，建议尽快套 nginx/caddy + 证书"
        ;;
      *)
        echo "✅ 安装完成！版本 $TAG"
        echo "   面板地址: http://${HEALTH_HOST}$( [ -n "${CFG_BASE:-}" ] && echo "（对外: $CFG_BASE，需自行配置反代/证书）" )"
        echo "   ⚠️  只监听本机，外部访问不了。要直连公网请改 QZ_LISTEN=0.0.0.0:${PORT} 后重启："
        echo "       sed -i 's|^QZ_LISTEN=.*|QZ_LISTEN=0.0.0.0:${PORT}|' $ENV_FILE && systemctl restart $SERVICE_NAME"
        ;;
    esac
    echo "   管理员:   ${CFG_ADMIN}"
    if [ "${PASS_GENERATED:-0}" = "1" ]; then
      echo "   初始密码: ${CFG_PASS}   ← 随机生成，请立即保存并在登录后修改"
    fi
    echo "   配置文件: $ENV_FILE（chmod 600，QZ_SECRET_KEY 请勿更换）"
  fi
  echo "   常用命令: systemctl status $SERVICE_NAME | journalctl -u $SERVICE_NAME -f"
  echo "   AI MCP:   $BIN_PATH token create --name ai-ops --preset mcp-admin --json"
  echo "   更新:     重跑本脚本，或用面板内「在线更新」"
  echo "   卸载:     bash $INSTALL_DIR/install.sh uninstall"
  if [ "$INIT_MCP" = "1" ]; then
    echo ""
    echo "========== 自动签发 AI 运维 MCP 密钥 =========="
    "$BIN_PATH" token create --name "ai-ops" --preset mcp-admin --env-file "$ENV_FILE" || true
  fi
else
  echo "⚠️  服务未确认启动成功，请查看日志:"
  echo "   journalctl -u $SERVICE_NAME -n 30 --no-pager"
  exit 1
fi

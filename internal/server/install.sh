#!/bin/sh
# LanTally agent installer. Usage:
#   sh -c "$(wget -qO- http://SERVER:8080/install.sh)" -- CLAIM_CODE
set -eu

SERVER_URL="__SERVER_URL__"
CODE=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --) shift ;;
    http://*|https://*) SERVER_URL="$1"; shift ;;
    *) CODE="$1"; shift ;;
  esac
done

if [ -z "$CODE" ]; then
  echo "usage: install.sh <claim-code>" >&2
  exit 1
fi

os="$(uname -s | tr 'A-Z' 'a-z')"
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  armv7l) arch="arm" ;;
  mipsel) arch="mipsle" ;;
  mips) arch="mips" ;;
esac
case "$os" in
  linux|darwin) ;;
  *) os="linux" ;;
esac

workdir="${TMPDIR:-/tmp}/lantally-install"
mkdir -p "$workdir"
bin="$workdir/lantally-agent"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$SERVER_URL/agents/${os}/${arch}" -o "$bin"
  body="$(curl -fsSL -X POST "$SERVER_URL/v1/claim" -H "Content-Type: application/json" -d "{\"code\":\"$CODE\"}")"
elif command -v wget >/dev/null 2>&1; then
  wget -qO "$bin" "$SERVER_URL/agents/${os}/${arch}"
  body="$(wget -qO- --header="Content-Type: application/json" --post-data="{\"code\":\"$CODE\"}" "$SERVER_URL/v1/claim")"
else
  echo "need curl or wget" >&2
  exit 1
fi
chmod 755 "$bin"

token="$(printf '%s' "$body" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
site_id="$(printf '%s' "$body" | sed -n 's/.*"site_id":"\([^"]*\)".*/\1/p')"
node_id="$(printf '%s' "$body" | sed -n 's/.*"node_id":"\([^"]*\)".*/\1/p')"
local_mode="$(printf '%s' "$body" | sed -n 's/.*"local":\([^,}]*\).*/\1/p')"
if [ -z "$token" ] || [ -z "$node_id" ]; then
  echo "claim failed: $body" >&2
  exit 1
fi

confdir="/etc/lantally"
if [ ! -w /etc ] && [ "$(id -u)" -ne 0 ]; then
  confdir="${HOME}/.config/lantally"
fi
mkdir -p "$confdir"
umask 077
printf '%s' "$token" >"$confdir/token"
chmod 600 "$confdir/token"

mihomo_url=""
mihomo_secret=""
for candidate in \
  /etc/openclash/config.yaml \
  /etc/openclash/clash.yaml \
  /etc/mihomo/config.yaml \
  "$HOME/.config/mihomo/config.yaml" \
  "$HOME/.config/clash/config.yaml"
do
  if [ -f "$candidate" ]; then
    secret="$(sed -n 's/^secret:[[:space:]]*//p' "$candidate" | head -n 1 | tr -d '"')"
    ext="$(sed -n 's/^external-controller:[[:space:]]*//p' "$candidate" | head -n 1 | tr -d '"')"
    if [ -n "$ext" ]; then
      case "$ext" in
        :*) mihomo_url="http://127.0.0.1$ext" ;;
        *) mihomo_url="http://$ext" ;;
      esac
      mihomo_secret="$secret"
      break
    fi
  fi
done
if [ -z "$mihomo_url" ] && [ "$local_mode" = "true" ]; then
  mihomo_url="http://127.0.0.1:9090"
fi

bindir="/usr/local/bin"
if [ ! -w /usr/local/bin ] 2>/dev/null; then
  bindir="$confdir"
fi
cp "$bin" "$bindir/lantally-agent"
chmod 755 "$bindir/lantally-agent"

mihomo_enabled="false"
if [ -n "$mihomo_url" ]; then
  mihomo_enabled="true"
  if [ -n "$mihomo_secret" ]; then
    printf '%s' "$mihomo_secret" >"$confdir/mihomo.secret"
    chmod 600 "$confdir/mihomo.secret"
  fi
fi

cat >"$confdir/agent.json" <<EOF
{
  "server_url": "$SERVER_URL",
  "site_id": "$site_id",
  "node_id": "$node_id",
  "token_file": "$confdir/token",
  "interval": "15s",
  "collectors": {"iface": true, "mihomo": $mihomo_enabled, "nlbwmon": false},
  "mihomo": {"url": "$mihomo_url", "secret_file": "$confdir/mihomo.secret"}
}
EOF
chmod 600 "$confdir/agent.json"

if [ -d /etc/init.d ] && [ "$(id -u)" -eq 0 ]; then
  cat >/etc/init.d/lantally-agent <<'INIT'
#!/bin/sh /etc/rc.common
USE_PROCD=1
START=95
start_service() {
  procd_open_instance
  procd_set_param command /usr/local/bin/lantally-agent -config /etc/lantally/agent.json
  procd_set_param respawn 3600 5 5
  procd_close_instance
}
INIT
  chmod 755 /etc/init.d/lantally-agent
  /etc/init.d/lantally-agent enable >/dev/null 2>&1 || true
  /etc/init.d/lantally-agent start >/dev/null 2>&1 || true
elif command -v systemctl >/dev/null 2>&1 && [ "$(id -u)" -eq 0 ]; then
  cat >/etc/systemd/system/lantally-agent.service <<EOF
[Unit]
Description=LanTally agent
After=network-online.target
[Service]
ExecStart=$bindir/lantally-agent -config $confdir/agent.json
Restart=always
[Install]
WantedBy=multi-user.target
EOF
  systemctl enable --now lantally-agent >/dev/null 2>&1 || true
elif [ "$os" = "darwin" ]; then
  launchdir="$HOME/Library/LaunchAgents"
  mkdir -p "$launchdir"
  cat >"$launchdir/com.lantally.agent.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.lantally.agent</string>
  <key>ProgramArguments</key><array>
    <string>$bindir/lantally-agent</string>
    <string>-config</string>
    <string>$confdir/agent.json</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict></plist>
EOF
  launchctl load "$launchdir/com.lantally.agent.plist" >/dev/null 2>&1 || true
else
  nohup "$bindir/lantally-agent" -config "$confdir/agent.json" >/dev/null 2>&1 &
fi

echo "LanTally agent installed for $node_id"

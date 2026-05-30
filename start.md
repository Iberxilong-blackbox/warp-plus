# Warp-Plus 本地启动与使用说明

本文档记录当前项目在 Windows 本地开发环境中的构建、运行、代理使用方式，以及本次排查中确认的关键问题。

## 0.快速回顾

启动并设置开机自启：

```bash
systemctl daemon-reload
systemctl enable --now warp-plus
systemctl status warp-plus
```

查看日志：

```bash
journalctl -u warp-plus -f
```

手动触发出口刷新：

```bash
curl -X POST http://127.0.0.1:9099/connectivity/refresh \
  -H "Authorization: Bearer $WARP_TOKEN"
```
# 先停掉 warp-plus 进程或 systemd 服务
# 如果你是前台跑的，Ctrl+C 即可

## 备份 + 清空recent_ips 的txt
```
cp /var/lib/warp-plus/pool-cache/recent_ips.txt \
   /var/lib/warp-plus/pool-cache/recent_ips.txt.bak.$(date +%Y%m%d-%H%M%S)

: > /var/lib/warp-plus/pool-cache/recent_ips.txt
 
systemctl restart warp-plus
 
```

## 创建/修改配置文件
```bash
nano /etc/default/warp-plus
```

## 1. 项目定位

`warp-plus` 是一个 Go 项目。普通模式下，它会建立 Cloudflare WARP 隧道，并在本机启动一个本地代理服务。

默认代理监听地址：

```text
127.0.0.1:10086
```

推荐链路：

```text
你的程序 -> 127.0.0.1:10086 -> warp-plus -> Cloudflare WARP -> 目标网站
```

不开 `--cfon` 时，它本质上是“本地代理形式的 WARP 出口”，不是官方 WARP 那种系统级 VPN。

## 2. 是否需要 Cloudflare 账号

不需要提供 Cloudflare 登录账号。

程序首次运行时会自动向 Cloudflare WARP API 注册一个 WARP 设备身份，并保存到本地缓存目录：

```text
C:\Users\<你的用户名>\AppData\Local\cache\warp-plus\primary\wgcf-identity.json
```

这个文件包含 WARP 私钥、设备 ID、token 等敏感信息，不要公开分享。

如果你有 WARP+ license，可以通过 `--key` 指定：

```powershell
.\warp-plus.exe --key "你的WARP_PLUS_LICENSE" --bind 127.0.0.1:10086
```

普通免费 WARP 不需要 `--key`。

## 3. Go 版本要求

当前项目 `go.mod` 中声明：

```text
go 1.24.0
toolchain go1.24.3
```

不要用 Go 1.26 构建当前版本。Go 1.26 会触发 `psiphon-tls` 依赖的兼容性问题：

```text
panic: tls: ConnectionState is not equal to tls.ConnectionState: struct field count mismatch
```

推荐强制使用 Go 1.24.3：

```powershell
$env:GOTOOLCHAIN="go1.24.3"
```

注意：`GOTOOLCHAIN=auto` 不会自动降级。如果本机是 Go 1.26，`auto` 会继续使用 Go 1.26。

## 4. Windows 构建命令

进入项目目录：

```powershell
cd C:\My_project\warp-plus
```

设置当前 PowerShell 会话的 Go 工具链和缓存目录：

```powershell
$env:GOTOOLCHAIN="go1.24.3"
$env:GOCACHE="$PWD\.gocache"
$env:GOMODCACHE="$PWD\.gomodcache"
```

说明：

```text
$PWD 表示当前目录。
如果当前目录是 C:\My_project\warp-plus，
那么 $PWD\.gocache 就是 C:\My_project\warp-plus\.gocache。
```

构建：

```powershell
go clean -cache
go mod download
go build -o warp-plus.exe .\cmd\warp-plus
```

检查 Go 实际版本：

```powershell
go version
```

应看到类似：

```text
go version go1.24.3 windows/amd64
```

## 5. 推荐启动命令

当前已验证可用的 Windows 启动命令：

```powershell
.\warp-plus.exe -4 --scan --test-url http://example.com/ --bind 127.0.0.1:10086
```

参数说明：

```text
-4
只使用 IPv4，避免本地 IPv6 网络质量干扰。

--scan
自动扫描可用的 Cloudflare WARP endpoint。

--test-url http://example.com/
用于启动前连通性检测。该地址对 HEAD 请求返回 200，适合当前项目的检测逻辑。

--bind 127.0.0.1:10086
本地代理监听地址和端口。
```

看到以下日志说明启动成功：

```text
connection test successful
serving proxy address=127.0.0.1:10086
```

如果没有出现 `serving proxy`，说明代理还没有真正启动。

## 6. 为什么要指定 `--test-url`

程序启动流程：

```text
1. 加载或注册 WARP identity
2. 建立 WireGuard/WARP 握手
3. 通过 WARP 隧道访问 test-url
4. test-url 返回 HTTP 200 后，才启动本地代理
```

默认测试地址是：

```text
http://connectivity.cloudflareclient.com/cdn-cgi/trace
```

本次测试中，默认地址和 Cloudflare trace 地址都曾导致：

```text
connection test failed
context deadline exceeded
```

而 `http://example.com/` 可以通过检测：

```text
connection test successful
serving proxy address=127.0.0.1:10086
```

原因是该项目检测逻辑比较严格：它会发起 `HEAD` 请求，并要求状态码必须是 `200`。如果测试 URL 返回 `301`、`302`、`403`、`204` 或超时，都会判定失败。

## 7. 如何使用本地代理

只要 `warp-plus.exe` 运行窗口还在，并且已经出现：

```text
serving proxy address=127.0.0.1:10086
```

其他程序就可以把代理指向：

```text
127.0.0.1:10086
```

优先使用 SOCKS5：

```text
socks5://127.0.0.1:10086
```

curl 测试：

```powershell
curl.exe --socks5 127.0.0.1:10086 http://example.com/
```

验证是否走 WARP：

```powershell
curl.exe --socks5 127.0.0.1:10086 https://api.ipify.org
curl.exe --socks5 127.0.0.1:10086 https://www.cloudflare.com/cdn-cgi/trace
```

不走代理时对比本机公网 IP：

```powershell
curl.exe https://api.ipify.org
```

如果不走代理是你的本机 IP，走代理后变成其他 IP，并且 Cloudflare trace 里出现：

```text
warp=on
```

说明请求已经通过 `warp-plus` 走了 Cloudflare WARP。

示例输出：

```text
ip=2a09:bac5:1f0b:2da5::48c:51
colo=LAX
loc=CN
warp=on
gateway=off
```

说明：

```text
warp=on 表示当前请求经过了 WARP。
colo=LAX 表示请求落到 Cloudflare 的 LAX 数据中心。
loc=CN 是 Cloudflare 判断的地区属性，不一定等同于出口机房所在地。
```

如果 `api.ipify.org` 返回 IPv4，而 Cloudflare trace 返回 IPv6，这是正常现象。

启动命令中的 `-4` 只表示本机连接 WARP endpoint 时使用 IPv4，不代表 WARP 隧道内部只能走 IPv4。WARP 隧道内部可以同时提供 IPv4 和 IPv6 出口。

多次请求返回相同 IP 也正常。WARP 不会每次请求随机换 IP，同一个 identity、endpoint、连接会话和 Cloudflare colo 下，出口 IP 可能保持稳定。

如果程序只支持 HTTP 代理环境变量：

```powershell
$env:HTTP_PROXY="http://127.0.0.1:10086"
$env:HTTPS_PROXY="http://127.0.0.1:10086"
```

部分程序只读取小写环境变量：

```powershell
$env:http_proxy="http://127.0.0.1:10086"
$env:https_proxy="http://127.0.0.1:10086"
```

注意不要写成：

```text
http_proxy = 127.0.0.1:10086
```

标准写法应带协议：

```text
http://127.0.0.1:10086
```

## 8. 日志含义

首次运行时看到：

```text
failed to load identity
creating new identity
successfully loaded warp identity
```

这是正常的。表示第一次没有本地身份文件，程序自动注册并保存了 WARP identity。

看到：

```text
Received handshake response
handshake complete
```

表示本机到 Cloudflare WARP endpoint 的 WireGuard 握手成功。

看到：

```text
connection test successful
serving proxy address=127.0.0.1:10086
```

表示代理已经可用。

看到：

```text
connection test failed
context deadline exceeded
```

表示 WARP 可能已经握手，但启动前连通性检测没有通过，代理不会启动。

## 9. keepalive 日志

如果使用 `-v`，会看到大量调试日志：

```text
Running tricks! (keepalive)
Sending keepalive packet
Retrying handshake because we stopped hearing back after 15 seconds
Received handshake response
```

这些通常不是错误。

它们表示 WireGuard 正在发送保活包，用于维持 UDP NAT 映射和 WARP 隧道状态。

正常运行不建议加 `-v`：

```powershell
.\warp-plus.exe -4 --scan --test-url http://example.com/ --bind 127.0.0.1:10086
```

## 10. `--cfon` 模式说明

不开 `--cfon`：

```text
你的程序 -> warp-plus -> Cloudflare WARP -> 互联网
```

开启 `--cfon`：

```text
你的程序 -> warp-plus -> WARP -> Psiphon -> 指定国家出口 -> 互联网
```

`--cfon` 可以指定国家，例如：

```powershell
.\warp-plus.exe --cfon --country US --bind 127.0.0.1:10086
```

当前 Windows 本地已验证成功的美国出口尝试命令：

```powershell
.\warp-plus.exe -4 --scan --cfon --country US --test-url http://example.com/ --bind 127.0.0.1:10086
```

建议保留 `--scan`。`--cfon` 模式需要先建立 WARP，再通过 WARP 启动 Psiphon；如果不扫描，随机选到的 WARP endpoint 可能无法完成握手。

但它会引入 Psiphon 网络，隐私和合规风险更高。当前目标只是让代理出口套 WARP，因此推荐先使用普通模式，不开 `--cfon`。

## 11. 安全注意事项

不要把本地代理直接暴露到公网：

```powershell
.\warp-plus.exe --bind 0.0.0.0:10086
```

该代理默认没有认证。监听公网会变成开放代理，容易被滥用。

推荐保持：

```text
127.0.0.1:10086
```

不要运行 `termux.sh` 中的第三方 Python 脚本：

```bash
wget -O wa.py https://raw.githubusercontent.com/Ptechgithub/configs/main/wa.py
python wa.py
```

该脚本用于模拟 WARP referral 增加 WARP+ 流量，不属于主项目源码，可能违反服务条款，也需要读取 WARP identity 信息。

## 12. Ubuntu 服务器部署

本章节用于把当前 `warp-plus` 部署到 Ubuntu 代理服务器上，并作为 s-ui / sing-box 的一个本机出口使用。

推荐链路：

```text
用户客户端 -> s-ui / sing-box 入站 -> sing-box SOCKS 出站 -> 127.0.0.1:10086 -> warp-plus -> Cloudflare WARP -> 目标网站
```

这里继续遵循一个简单原则：

```text
root 用户负责安装、构建、写 systemd 配置。
warp-plus 专用系统用户负责长期运行 warp-plus 服务。
```

原因：

```text
warp-plus 只需要监听 127.0.0.1:10086，不需要 root 权限。
长期用 root 跑网络代理会扩大风险面。
使用专用用户可以把运行权限限制在 /var/lib/warp-plus 和本服务本身。
```

这也符合：

```text
KISS：保持本地端口、systemd 托管和清晰的进程边界。
YAGNI：不额外引入 Docker、supervisor 或复杂脚本。
SRP：root 只做管理，warp-plus 用户只负责运行服务。
DRY：s-ui 继续负责用户和路由分流，warp-plus 只负责 WARP 出口。
```

当前开启 control/pool 后，`warp-plus` 会由主进程管理 child 进程池：

```text
KISS：仍然只暴露一个本机业务端口 127.0.0.1:10086 和一个本机 control 端口 127.0.0.1:9099。
YAGNI：不引入额外守护程序，进程池由 warp-plus 自身管理。
SRP：parent 负责 relay/control/pool，child 负责独立 WARP + Psiphon + egresscheck。
DRY：出口检查、黑名单、recent IP 规则复用同一套 CLI 配置。
```

### 12.1 确认服务器架构

当前服务器执行：

```bash
uname -m
```

如果输出是：

```text
x86_64
```

说明是 AMD64 架构，Go 安装包使用：

```text
go1.24.3.linux-amd64.tar.gz
```

### 12.2 安装 Go 1.24.3

当前项目 `go.mod` 要求：

```text
go 1.24.0
toolchain go1.24.3
```

Ubuntu 服务器上不要直接用系统仓库里的旧版 Go，也不要用 Go 1.26。

使用 root 执行：

```bash
apt update
apt install -y curl ca-certificates tar git build-essential

cd /tmp
curl -LO https://go.dev/dl/go1.24.3.linux-amd64.tar.gz

rm -rf /usr/local/go
tar -C /usr/local -xzf go1.24.3.linux-amd64.tar.gz

echo 'export PATH=/usr/local/go/bin:$PATH' > /etc/profile.d/go.sh
chmod 0644 /etc/profile.d/go.sh
export PATH=/usr/local/go/bin:$PATH

go version
```

应看到类似：

```text
go version go1.24.3 linux/amd64
```

### 12.3 获取项目源码

如果服务器可以直接访问你的仓库：

```bash
cd /opt
git clone <你的仓库地址> warp-plus
cd /opt/warp-plus
```

如果服务器不能访问仓库，可以从 Windows 把当前项目上传到：

```text
/opt/warp-plus
```

上传后确认目录中至少包含：

```text
go.mod
go.sum
cmd/
app/
warp/
wiresocks/
```

### 12.4 构建 Linux 可执行文件

使用 root 执行：

```bash
cd /opt/warp-plus

export PATH=/usr/local/go/bin:$PATH
export GOTOOLCHAIN=go1.24.3
export GOCACHE="$PWD/.gocache"
export GOMODCACHE="$PWD/.gomodcache"

go mod download
go build -o warp-plus ./cmd/warp-plus

install -m 0755 ./warp-plus /usr/local/bin/warp-plus
```

检查二进制是否可运行：

```bash
/usr/local/bin/warp-plus --help
```

注意：

```text
当前本地构建版本可能不支持 --version。
如果执行 /usr/local/bin/warp-plus --version 看到 unknown flag "version"，不代表编译失败，只表示该参数没有注册。
```

### 12.5 创建专用运行用户

使用 root 执行：

```bash
useradd --system --home /var/lib/warp-plus --create-home --shell /usr/sbin/nologin warp-plus
install -d -o warp-plus -g warp-plus /var/lib/warp-plus
```

说明：

```text
/var/lib/warp-plus
用于保存 WARP identity、缓存和运行数据。

warp-plus 用户
只用于运行服务，不用于 SSH 登录。
```

### 12.6 手动测试服务

正式写 systemd 前，先手动跑一次，确认服务器网络可以建立 WARP、Psiphon 指定国家出口、出口 IP 检查、control API 和 pool 子进程都能正常工作。

使用 root 执行：

```bash
export WARP_PLUS=/usr/local/bin/warp-plus
export WARP_DATA=/var/lib/warp-plus
export WARP_BLACKLIST=/opt/warp-plus/blacklist.example.txt
export WARP_ENDPOINT=188.114.98.7:3581
export WARP_BIND=127.0.0.1:10086
export WARP_CONTROL=127.0.0.1:9099
export WARP_TOKEN=test-token

install -d -o warp-plus -g warp-plus "$WARP_DATA/pool-cache"

sudo -u warp-plus env \
  WARP_PLUS="$WARP_PLUS" \
  WARP_DATA="$WARP_DATA" \
  WARP_BLACKLIST="$WARP_BLACKLIST" \
  WARP_ENDPOINT="$WARP_ENDPOINT" \
  WARP_BIND="$WARP_BIND" \
  WARP_CONTROL="$WARP_CONTROL" \
  WARP_TOKEN="$WARP_TOKEN" \
  bash -lc '
"$WARP_PLUS" \
  -4 \
  --endpoint "$WARP_ENDPOINT" \
  --cfon \
  --country US \
  --test-url http://example.com/ \
  --bind "$WARP_BIND" \
  --cache-dir "$WARP_DATA/pool-cache" \
  --egress-check \
  --egress-blacklist "$WARP_BLACKLIST" \
  --egress-max-retry 2 \
  --egress-check-interval 0 \
  --control-bind "$WARP_CONTROL" \
  --control-token "$WARP_TOKEN" \
  --recent-ip-file "$WARP_DATA/pool-cache/recent_ips.txt" \
  --recent-ip-limit 50 \
  --pool-min-ready 1 \
  --verbose 2>&1 | tee "$WARP_DATA/pool-server-entry.log"
'
```

说明：

```text
--endpoint "$WARP_ENDPOINT"
固定一个已验证可用的 Cloudflare WARP endpoint，避免每次启动都受 scan 波动影响。

--cfon --country US
表示启用 Psiphon 模式，并尝试使用 US 作为最终出口国家。

--cache-dir "$WARP_DATA/pool-cache"
pool 模式的主缓存目录。子进程会在该目录下创建独立 child_N 数据目录，避免 Psiphon data store 冲突。

--egress-check
启用最终出口 IP 检查。当前只支持和 --cfon 一起使用。

--egress-blacklist "$WARP_BLACKLIST"
出口 IP 黑名单文件，支持单 IP、CIDR 和简单前缀规则。

--egress-max-retry 2
每个 child 选择出口时最多尝试 2 次，避免无限等待不可用 Psiphon 节点。

--egress-check-interval 0
关闭运行期定时出口检查。当前主要依赖手动 control API 触发刷新。

--control-bind "$WARP_CONTROL"
启用本机 control API，用于 health 检查和手动刷新出口。

--control-token "$WARP_TOKEN"
control API 的 Bearer token。即使只监听 127.0.0.1，也建议保留。

--recent-ip-file "$WARP_DATA/pool-cache/recent_ips.txt"
记录最近使用过的出口 IP，避免 refresh 后马上切回旧出口。

--recent-ip-limit 50
最多保留 50 个近期出口 IP。

--pool-min-ready 1
保持至少 1 个 ready child，refresh 时可以直接切换到已预热出口。
```

看到以下日志说明服务启动成功：

```text
connection test successful
child registry listening
child activated (first)
pool maintenance started
serving rotate control address=127.0.0.1:9099
serving proxy address=127.0.0.1:10086
```

另开一个 SSH 窗口测试。如果新窗口没有上面的环境变量，先补充：

```bash
export WARP_DATA=/var/lib/warp-plus
export WARP_TOKEN=test-token
```

再执行：

```bash
curl --socks5-hostname 127.0.0.1:10086 https://api.ipify.org
curl --socks5-hostname 127.0.0.1:10086 https://ipinfo.io/json
curl http://127.0.0.1:9099/health
curl -X POST http://127.0.0.1:9099/connectivity/refresh \
  -H "Authorization: Bearer $WARP_TOKEN"
cat "$WARP_DATA/pool-cache/recent_ips.txt"
```

如果能正常返回 IP 或 JSON，且 `/health` 返回 `{"status":"ok"}`，说明服务器本机已经可以通过 `warp-plus` 走当前代理出口，并且 control API 已可用。

注意：

```text
当前 12.6 使用的是 --cfon --country US。
此时最终出口通常是 Psiphon 节点，不是 Cloudflare WARP 节点。
因此不要用 warp=on 作为 cfon 模式的唯一判断标准。
```

如果测试普通 WARP 模式，也就是去掉 `--cfon --country US` 后启动，可以再检查 Cloudflare trace：

```bash
curl --socks5-hostname 127.0.0.1:10086 https://www.cloudflare.com/cdn-cgi/trace
```

普通 WARP 模式下，如果 Cloudflare trace 中出现：

```text
warp=on
```

说明服务器本机已经可以通过 `warp-plus` 走 WARP 出口。

`POST /connectivity/refresh` 的常见返回：

```text
{"status":"ok"}
刷新成功，新连接会走新出口。

{"status":"busy"}
当前没有 ready child，旧 active 仍继续服务，稍后再试。

{"status":"no_acceptable_egress"}
ready child 的出口命中 same/recent/blacklist 等规则，已被丢弃，旧 active 仍继续服务。
```

如果某个站点返回：

```text
curl: (97) Can't complete SOCKS5 connection to <domain>. (5)
```

含义通常是：

```text
curl 已经连上 127.0.0.1:10086。
但当前代理出口连接这个目标域名失败。
这不是 10086 端口没有启动，也不一定是 curl 命令写错。
```

测试完成后按 `Ctrl+C` 停止手动进程。

### 12.7 使用 systemd 托管

先创建参数文件：

```bash
nano /etc/default/warp-plus
```

写入：

```bash
# warp-plus 启动参数和运行路径。
# 修改本文件后，只需要执行：systemctl restart warp-plus
# 只有修改 /etc/systemd/system/warp-plus.service 后，才需要执行：systemctl daemon-reload

WARP_ENDPOINT=188.114.98.7:3581
WARP_DATA=/var/lib/warp-plus
WARP_BLACKLIST=/opt/warp-plus/blacklist.example.txt
WARP_BIND=127.0.0.1:10086
WARP_CONTROL=127.0.0.1:9099
WARP_TOKEN=replace-with-random-long-token
WARP_COUNTRY=US
```

说明：

```text
/etc/default/warp-plus
只放容易变化的参数。后续改 endpoint、端口、国家、control token、黑名单路径，优先改这个文件。

warp-plus.service
只负责指定运行用户、工作目录、重启策略和调用程序本体。
```

参数含义：

```text
WARP_ENDPOINT
固定的 Cloudflare WARP endpoint。建议先通过前台普通 WARP 验证后再写入。

WARP_DATA
服务数据根目录。当前 pool 模式实际使用 $WARP_DATA/pool-cache。

WARP_BLACKLIST
出口 IP 黑名单文件。启用 --egress-check 时会加载该文件。

WARP_BIND
业务 SOCKS/HTTP 代理监听地址。必须保持 127.0.0.1，不要暴露到公网。

WARP_CONTROL
control API 监听地址。默认建议保持 127.0.0.1:9099。
如果需要从另一台电脑直接发送 HTTP POST，可以改成 0.0.0.0:9099，但必须配合防火墙白名单。

WARP_TOKEN
control API Bearer token。不要使用示例值，生产环境必须替换。
即使已经设置来源 IP 白名单，也建议使用长随机 token。白名单负责限制谁能连到端口，token 负责限制谁能调用接口。

WARP_COUNTRY
Psiphon 目标出口国家，例如 US、JP、DE。
```

创建 pool 缓存目录并设置权限：

```bash
. /etc/default/warp-plus
install -d -o warp-plus -g warp-plus "$WARP_DATA/pool-cache"
```

再创建服务文件：

```bash
nano /etc/systemd/system/warp-plus.service
```

写入：

```ini
[Unit]
Description=warp-plus pooled WARP/Psiphon outbound proxy
After=network-online.target
Wants=network-online.target

[Service]
User=warp-plus
Group=warp-plus
WorkingDirectory=/var/lib/warp-plus
EnvironmentFile=/etc/default/warp-plus
ExecStart=/usr/local/bin/warp-plus \
  -4 \
  --endpoint ${WARP_ENDPOINT} \
  --cfon \
  --country ${WARP_COUNTRY} \
  --test-url http://example.com/ \
  --bind ${WARP_BIND} \
  --cache-dir ${WARP_DATA}/pool-cache \
  --egress-check \
  --egress-blacklist ${WARP_BLACKLIST} \
  --egress-max-retry 2 \
  --egress-check-interval 0 \
  --control-bind ${WARP_CONTROL} \
  --control-token ${WARP_TOKEN} \
  --recent-ip-file ${WARP_DATA}/pool-cache/recent_ips.txt \
  --recent-ip-limit 50 \
  --pool-min-ready 1 \
  --verbose
Restart=always
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

说明：

```text
pool-cache 目录由 root 在创建 service 前手动准备。
service 本身只负责以 warp-plus 用户运行主进程，避免 ExecStartPre 权限差异导致启动失败。

systemd 托管时不需要在 ExecStart 里写 2>&1 | tee。
stdout/stderr 会进入 journald，用 journalctl -u warp-plus -f 查看。

如果必须额外落文件，优先用 journald 持久化或 logrotate 管理，不建议把 shell 管道作为服务主进程。
```

#### 12.7.1 允许另一台电脑远程触发 refresh

如果只在服务器本机执行：

```bash
curl -X POST http://127.0.0.1:9099/connectivity/refresh \
  -H "Authorization: Bearer $WARP_TOKEN"
```

保持默认配置即可：

```bash
WARP_CONTROL=127.0.0.1:9099
```

如果电脑 A 上有程序需要频繁触发 refresh，优先使用 SSH 隧道，而不是把 `9099` 暴露公网。

思路：

```text
电脑 A 程序启动前，先建立一条 SSH 本地端口转发。
电脑 A 本地端口，例如 127.0.0.1:19099，转发到服务器 B 的 127.0.0.1:9099。
程序运行期间保持 SSH 隧道不关闭。
程序需要切换出口时，请求 http://127.0.0.1:19099/connectivity/refresh。
程序结束时关闭 SSH 隧道。
```

链路：

```text
电脑 A 程序
-> 电脑 A 127.0.0.1:19099
-> SSH 隧道
-> 服务器 B 127.0.0.1:9099
-> warp-plus control API
```

这种方式下服务器仍然保持：

```bash
WARP_CONTROL=127.0.0.1:9099
```

优点：

```text
9099 不暴露公网。
不需要给 9099 配公网白名单。
频繁 refresh 只是在已有 SSH 隧道里发送普通 HTTP 请求，不需要每次重新建立 SSH 连接。
适合由电脑 A 上的程序在运行期间反复触发出口切换。
```

注意：

```text
不要每次 refresh 都新建 SSH 隧道。
应在程序启动时建立一次，程序运行期间复用，程序退出时关闭。
电脑 A 建议使用 SSH key 登录服务器 B，避免程序卡在密码输入。
```

如果明确需要从另一台电脑直接发送公网 HTTP POST，让代理服务器切换出口 IP，也可以把 control API 改成公网监听：

```bash
WARP_CONTROL=0.0.0.0:9099
```

这种方式必须限制 `9099` 只允许你的控制电脑访问。假设你的控制电脑公网 IP 是：

```text
203.0.113.10
```

使用 UFW 白名单：

```bash
ufw allow from 203.0.113.10 to any port 9099 proto tcp
ufw deny 9099/tcp
ufw status numbered
```

说明：

```text
UFW 是 Ubuntu 常用防火墙工具。
如果你的云厂商安全组已经限制了 9099 来源 IP，也可以用安全组实现同样效果。
最稳妥做法是：云安全组限制一次，服务器 UFW 再限制一次。
```

修改 `/etc/default/warp-plus` 后重启：

```bash
systemctl restart warp-plus
ss -lntp | grep 9099
```

如果看到：

```text
0.0.0.0:9099
```

说明 control API 已监听所有网卡。此时应再确认 UFW 或云安全组已经限制来源 IP。

远程电脑测试：

```bash
curl http://服务器IP:9099/health

curl -X POST http://服务器IP:9099/connectivity/refresh \
  -H "Authorization: Bearer 你的WARP_TOKEN"
```

生成长随机 token：

```bash
openssl rand -hex 32
```

不要因为设置了白名单就使用短 token。原因是：

```text
白名单可能因为公网 IP 变化、NAT、云安全组误配置而失效。
HTTP 请求中的 token 仍是 control API 的最后一道鉴权。
长随机 token 的成本很低，但能避免端口误暴露时被直接调用。
```

启动并设置开机自启：

```bash
systemctl daemon-reload
systemctl enable --now warp-plus
systemctl status warp-plus
```

查看日志：

```bash
journalctl -u warp-plus -f
```

确认端口只监听本机：

```bash
ss -lntp | grep 10086
```

应看到类似：

```text
127.0.0.1:10086
```

不要改成：

```text
0.0.0.0:10086
```

因为 `warp-plus` 暴露的本地代理默认没有认证，直接监听公网会变成开放代理。

### 12.8 s-ui / sing-box 中添加 warp-plus 出站

`warp-plus` 启动后，s-ui / sing-box 只需要把它当成本机 SOCKS5 上游代理。

新增一个 SOCKS 出站，核心配置是：

```json
{
  "type": "socks",
  "tag": "warp-plus-out",
  "server": "127.0.0.1",
  "server_port": 10086,
  "version": "5"
}
```

含义：

```text
tag
后续路由规则引用的出站名称。

server / server_port
指向本机 warp-plus 监听地址。

version
使用 SOCKS5。
```

### 12.9 让一部分用户走 warp-plus 出口

目标是：

```text
指定用户 -> warp-plus-out
其他用户 -> 原来的默认出口
```

如果 s-ui 的高级配置允许直接编辑 sing-box JSON，可以在 route rules 中加入类似规则：

```json
{
  "auth_user": [
    "user_a",
    "user_b"
  ],
  "action": "route",
  "outbound": "warp-plus-out"
}
```

并保留默认出口，例如：

```json
{
  "route": {
    "rules": [
      {
        "auth_user": [
          "user_a",
          "user_b"
        ],
        "action": "route",
        "outbound": "warp-plus-out"
      }
    ],
    "final": "direct"
  }
}
```

注意：

```text
auth_user 需要和 sing-box 入站里对应用户的 name / username 匹配。
不同入站协议在 s-ui UI 中展示的字段可能不同。
如果 UI 没有暴露按用户路由，就需要在高级 JSON 配置中修改。
```

如果不是按用户，而是按目标域名走 WARP，可以使用域名规则：

```json
{
  "domain_suffix": [
    "openai.com",
    "chatgpt.com"
  ],
  "action": "route",
  "outbound": "warp-plus-out"
}
```

### 12.10 验证 s-ui 链式代理是否生效

验证顺序：

```text
1. 先确认 warp-plus 本机代理可用。
2. 再确认 s-ui / sing-box 配置保存并重启成功。
3. 用被分流的用户连接代理。
4. 访问出口 IP 查询站点，确认被分流用户的出口与服务器直连出口不同。
```

服务器本机测试：

```bash
curl --socks5-hostname 127.0.0.1:10086 https://api.ipify.org
curl --socks5-hostname 127.0.0.1:10086 https://ipinfo.io/json
```

客户端侧测试：

```text
https://api.ipify.org
https://ipinfo.io/json
```

如果使用普通 WARP 模式，可以额外访问：

```text
https://www.cloudflare.com/cdn-cgi/trace
```

普通 WARP 模式下看到 `warp=on`，说明链路已经是：

```text
用户客户端 -> s-ui / sing-box -> warp-plus -> Cloudflare WARP -> 目标网站
```

如果使用当前推荐的 `--cfon --country US`，最终出口通常是 Psiphon 节点，`warp=on` 不能作为唯一判断标准。此时优先看 `api.ipify.org` / `ipinfo.io` 返回的出口 IP 和国家信息，并用 control API refresh 前后对比出口是否变化。

### 12.11 可选：切换国家或切回普通 WARP

普通模式：

```text
s-ui -> warp-plus -> Cloudflare WARP -> 目标网站
```

`--cfon` 模式：

```text
s-ui -> warp-plus -> WARP -> Psiphon -> 指定国家出口 -> 目标网站
```

当前 systemd 示例默认使用 `--cfon --country ${WARP_COUNTRY}`、出口检查、control API 和 pool。要切换国家，只需要修改参数文件：

```bash
nano /etc/default/warp-plus
```

例如把：

```bash
WARP_COUNTRY=US
```

改成：

```bash
WARP_COUNTRY=JP
```

然后重启：

```bash
systemctl restart warp-plus
journalctl -u warp-plus -f
```

如果只想切回普通 WARP 模式，需要修改 `/etc/systemd/system/warp-plus.service`，移除这些只适用于 cfon/pool 的参数：

```text
--cfon
--country ${WARP_COUNTRY}
--egress-check
--egress-blacklist ${WARP_BLACKLIST}
--egress-max-retry 2
--egress-check-interval 0
--control-bind ${WARP_CONTROL}
--control-token ${WARP_TOKEN}
--recent-ip-file ${WARP_DATA}/pool-cache/recent_ips.txt
--recent-ip-limit 50
--pool-min-ready 1
--verbose
```

保留最小普通 WARP 参数：

```ini
ExecStart=/usr/local/bin/warp-plus \
  -4 \
  --endpoint ${WARP_ENDPOINT} \
  --test-url http://example.com/ \
  --bind ${WARP_BIND} \
  --cache-dir ${WARP_DATA}/normal-warp-cache
```

修改 service 文件后执行：

```bash
systemctl daemon-reload
systemctl restart warp-plus
```

建议：

```text
如果目标是固定国家出口和手动刷新 IP，继续使用当前 cfon + egress-check + control/pool 配置。
如果目标只是让一部分流量套 WARP，不需要指定国家出口，可以切回普通 WARP 模式。
--cfon 会引入 Psiphon 网络，信任边界和故障点都比普通 WARP 更多。
```

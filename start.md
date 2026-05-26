# Warp-Plus 本地启动与使用说明

本文档记录当前项目在 Windows 本地开发环境中的构建、运行、代理使用方式，以及本次排查中确认的关键问题。

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
KISS：保持单进程、本地端口、systemd 托管。
YAGNI：不额外引入 Docker、supervisor 或复杂脚本。
SRP：root 只做管理，warp-plus 用户只负责运行服务。
DRY：s-ui 继续负责用户和路由分流，warp-plus 只负责 WARP 出口。
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

正式写 systemd 前，先手动跑一次，确认服务器网络可以建立 WARP，并顺便测试指定国家出口用法。

使用 root 执行：

```bash
sudo -u warp-plus /usr/local/bin/warp-plus \
  -4 \
  --scan \
  --cfon \
  --country US \
  --test-url http://example.com/ \
  --bind 127.0.0.1:10086 \
  --cache-dir /var/lib/warp-plus
```

说明：

```text
--cfon --country US
表示启用 Psiphon 模式，并尝试使用 US 作为最终出口国家。

如果只想测试普通 WARP 出口，可以去掉 --cfon 和 --country US。
```

看到以下日志说明服务启动成功：

```text
connection test successful
serving proxy address=127.0.0.1:10086
```

另开一个 SSH 窗口测试：

```bash
curl --socks5-hostname 127.0.0.1:10086 https://api.ipify.org
curl --socks5-hostname 127.0.0.1:10086 https://ipinfo.io/json
```

如果能正常返回 IP 或 JSON，说明服务器本机已经可以通过 `warp-plus` 走当前代理出口。

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
# warp-plus 启动参数。
# 修改本文件后，只需要执行：systemctl restart warp-plus
# 只有修改 /etc/systemd/system/warp-plus.service 后，才需要执行：systemctl daemon-reload

# -4
# 只使用 IPv4 连接 Cloudflare WARP endpoint。服务器 IPv6 不稳定时建议保留。
#
# --scan
# 启动时扫描可用的 Cloudflare WARP endpoint，选择质量较好的入口。
#
# --test-url http://example.com/
# 启动前连通性检测地址。该项目要求测试地址对 HEAD 请求返回 HTTP 200。
#
# --bind 127.0.0.1:10086
# 本地 SOCKS/HTTP 代理监听地址。必须只监听 127.0.0.1，不要暴露到公网。
#
# --cache-dir /var/lib/warp-plus
# WARP identity 和运行缓存目录。该目录归 warp-plus 专用用户使用。
#
# --cfon
# 可选。启用 Psiphon 模式。链路变为：s-ui -> warp-plus -> WARP -> Psiphon -> 目标网站。
#
    # --country US
    # 可选。开启cfon参数欧，指定 Psiphon 最终出口国家为 US。只有开启 --cfon 时才有意义。
#
# 普通 WARP 模式：
WARP_PLUS_ARGS="-4 --scan --test-url http://example.com/ --bind 127.0.0.1:10086 --cache-dir /var/lib/warp-plus --cfon --country US"

# 如果要使用 US 国家出口，注释上一行，取消下一行注释：
# WARP_PLUS_ARGS="-4 --scan --cfon --country US --test-url http://example.com/ --bind 127.0.0.1:10086 --cache-dir /var/lib/warp-plus"
```

说明：

```text
/etc/default/warp-plus
只放启动参数，后续改端口、改国家、切换普通模式和 cfon 模式，优先改这个文件。

warp-plus.service
只负责指定运行用户、工作目录、重启策略和调用程序本体。
```

再创建服务文件：

```bash
nano /etc/systemd/system/warp-plus.service
```

写入：

```ini
[Unit]
Description=warp-plus local WARP outbound proxy
After=network-online.target
Wants=network-online.target

[Service]
User=warp-plus
Group=warp-plus
WorkingDirectory=/var/lib/warp-plus
EnvironmentFile=/etc/default/warp-plus
ExecStart=/usr/local/bin/warp-plus $WARP_PLUS_ARGS
Restart=always
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
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
4. 访问 Cloudflare trace 检查 warp=on。
```

服务器本机测试：

```bash
curl --socks5 127.0.0.1:10086 https://www.cloudflare.com/cdn-cgi/trace
```

客户端侧测试：

```text
https://www.cloudflare.com/cdn-cgi/trace
```

如果被分流用户看到：

```text
warp=on
```

说明链路已经是：

```text
用户客户端 -> s-ui / sing-box -> warp-plus -> Cloudflare WARP -> 目标网站
```

### 12.11 可选：cfon 模式

普通模式：

```text
s-ui -> warp-plus -> Cloudflare WARP -> 目标网站
```

`--cfon` 模式：

```text
s-ui -> warp-plus -> WARP -> Psiphon -> 指定国家出口 -> 目标网站
```

如果确实需要指定 Psiphon 出口国家，优先修改参数文件：

```bash
nano /etc/default/warp-plus
```

把 `WARP_PLUS_ARGS` 改成：

```bash
WARP_PLUS_ARGS="-4 --scan --cfon --country US --test-url http://example.com/ --bind 127.0.0.1:10086 --cache-dir /var/lib/warp-plus"
```

然后重启：

```bash
systemctl restart warp-plus
journalctl -u warp-plus -f
```

如果只想切回普通 WARP 模式，就改回：

```bash
WARP_PLUS_ARGS="-4 --scan --test-url http://example.com/ --bind 127.0.0.1:10086 --cache-dir /var/lib/warp-plus"
```

再执行：

```bash
systemctl restart warp-plus
```

建议：

```text
第一阶段优先使用普通 WARP 模式。
只有明确需要指定国家出口时，再开启 --cfon。
--cfon 会引入 Psiphon 网络，信任边界和故障点都会增加。
```

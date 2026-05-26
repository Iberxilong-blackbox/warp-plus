# Warp-Plus 本地启动与使用说明

本文档记录当前项目在 Windows 本地开发环境中的构建、运行、代理使用方式，以及本次排查中确认的关键问题。

## 1. 项目定位

`warp-plus` 是一个 Go 项目。普通模式下，它会建立 Cloudflare WARP 隧道，并在本机启动一个本地代理服务。

默认代理监听地址：

```text
127.0.0.1:8086
```

推荐链路：

```text
你的程序 -> 127.0.0.1:8086 -> warp-plus -> Cloudflare WARP -> 目标网站
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
.\warp-plus.exe --key "你的WARP_PLUS_LICENSE" --bind 127.0.0.1:8086
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
.\warp-plus.exe -4 --scan --test-url http://example.com/ --bind 127.0.0.1:8086
```

参数说明：

```text
-4
只使用 IPv4，避免本地 IPv6 网络质量干扰。

--scan
自动扫描可用的 Cloudflare WARP endpoint。

--test-url http://example.com/
用于启动前连通性检测。该地址对 HEAD 请求返回 200，适合当前项目的检测逻辑。

--bind 127.0.0.1:8086
本地代理监听地址和端口。
```

看到以下日志说明启动成功：

```text
connection test successful
serving proxy address=127.0.0.1:8086
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
serving proxy address=127.0.0.1:8086
```

原因是该项目检测逻辑比较严格：它会发起 `HEAD` 请求，并要求状态码必须是 `200`。如果测试 URL 返回 `301`、`302`、`403`、`204` 或超时，都会判定失败。

## 7. 如何使用本地代理

只要 `warp-plus.exe` 运行窗口还在，并且已经出现：

```text
serving proxy address=127.0.0.1:8086
```

其他程序就可以把代理指向：

```text
127.0.0.1:8086
```

优先使用 SOCKS5：

```text
socks5://127.0.0.1:8086
```

curl 测试：

```powershell
curl.exe --socks5 127.0.0.1:8086 http://example.com/
```

验证是否走 WARP：

```powershell
curl.exe --socks5 127.0.0.1:8086 https://api.ipify.org
curl.exe --socks5 127.0.0.1:8086 https://www.cloudflare.com/cdn-cgi/trace
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
$env:HTTP_PROXY="http://127.0.0.1:8086"
$env:HTTPS_PROXY="http://127.0.0.1:8086"
```

部分程序只读取小写环境变量：

```powershell
$env:http_proxy="http://127.0.0.1:8086"
$env:https_proxy="http://127.0.0.1:8086"
```

注意不要写成：

```text
http_proxy = 127.0.0.1:8086
```

标准写法应带协议：

```text
http://127.0.0.1:8086
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
serving proxy address=127.0.0.1:8086
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
.\warp-plus.exe -4 --scan --test-url http://example.com/ --bind 127.0.0.1:8086
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
.\warp-plus.exe --cfon --country US --bind 127.0.0.1:8086
```

当前 Windows 本地已验证成功的美国出口尝试命令：

```powershell
.\warp-plus.exe -4 --scan --cfon --country US --test-url http://example.com/ --bind 127.0.0.1:8086
```

建议保留 `--scan`。`--cfon` 模式需要先建立 WARP，再通过 WARP 启动 Psiphon；如果不扫描，随机选到的 WARP endpoint 可能无法完成握手。

但它会引入 Psiphon 网络，隐私和合规风险更高。当前目标只是让代理出口套 WARP，因此推荐先使用普通模式，不开 `--cfon`。

## 11. 安全注意事项

不要把本地代理直接暴露到公网：

```powershell
.\warp-plus.exe --bind 0.0.0.0:8086
```

该代理默认没有认证。监听公网会变成开放代理，容易被滥用。

推荐保持：

```text
127.0.0.1:8086
```

不要运行 `termux.sh` 中的第三方 Python 脚本：

```bash
wget -O wa.py https://raw.githubusercontent.com/Ptechgithub/configs/main/wa.py
python wa.py
```

该脚本用于模拟 WARP referral 增加 WARP+ 流量，不属于主项目源码，可能违反服务条款，也需要读取 WARP identity 信息。

## 12. 后续 Ubuntu 部署

Windows 本地验证通过后，再单独整理 Ubuntu 服务器部署方式。服务器上建议：

```text
1. 从源码构建
2. 使用专用系统用户运行
3. 缓存目录放到 /var/lib/warp-plus
4. 通过 systemd 托管
5. 仍然只监听 127.0.0.1:8086
```

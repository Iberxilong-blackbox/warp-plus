# Ubuntu warp-plus 前台验证清单

本文档用于把 Windows 本地诊断后的下一步迁移到 Ubuntu 服务器上验证。当前 Windows 结论是：

```text
WARP 阶段可用。
US / SG / JP 旧 cfon 单进程都在 Psiphon handshake 阶段超时。
pool 不是当前主因，下一步应先在 Ubuntu 前台验证旧 cfon。
```

核心目标：

```text
先确认 Ubuntu 上旧 cfon 能否跑通。
只有旧 cfon 跑通后，才继续验证 pool。
只有 pool 前台稳定后，才进入 systemd 托管和 S-UI / sing-box 接入。
```

## 0. 准备变量

以下命令假设：

- 程序路径：`/opt/warp-plus/warp-plus`
- 数据目录：`/var/lib/warp-plus`
- 业务 SOCKS 端口：`127.0.0.1:10086`
- 控制端口：`127.0.0.1:9099`
- control token：`test-token`
- blacklist 文件：`/opt/warp-plus/blacklist.example.txt`
- 固定 WARP endpoint：`188.114.98.7:3581`

如果你的文件位置不同，先替换变量：

```bash
export WARP_PLUS=/opt/warp-plus/warp-plus
export WARP_DATA=/var/lib/warp-plus
export WARP_BLACKLIST=/opt/warp-plus/blacklist.example.txt
export WARP_ENDPOINT=188.114.98.7:3581
export WARP_BIND=127.0.0.1:10086
export WARP_CONTROL=127.0.0.1:9099
export WARP_TOKEN=test-token
```

## 1. 基础文件和权限检查

目的：

```text
先排除最基础的“文件不存在 / 不可执行 / 数据目录不可写”问题。
Windows 已经遇到过 cache ACL 问题，Ubuntu 上必须先确认 /var/lib/warp-plus 可写。
```

命令：

```bash
ls -lh "$WARP_PLUS"
ls -lh "$WARP_BLACKLIST"
mkdir -p "$WARP_DATA"
test -x "$WARP_PLUS"
test -w "$WARP_DATA"
```

如果 `test -w "$WARP_DATA"` 失败，需要按你的运行用户修权限。例如当前用 root 前台验证：

```bash
sudo mkdir -p "$WARP_DATA"
sudo chmod 700 "$WARP_DATA"
```

如果后续准备用专用用户 `warp-plus` 跑：

```bash
sudo useradd --system --home "$WARP_DATA" --shell /usr/sbin/nologin warp-plus
sudo mkdir -p "$WARP_DATA"
sudo chown -R warp-plus:warp-plus "$WARP_DATA"
sudo chmod 700 "$WARP_DATA"
```

## 2. 普通 WARP 前台验证

目的：

```text
确认固定 endpoint 当前在 Ubuntu 网络环境下可用。
如果普通 WARP 都不通，就不要继续测 cfon 或 pool。
```

命令：

```bash
"$WARP_PLUS" -4 \
  --endpoint "$WARP_ENDPOINT" \
  --test-url http://example.com/ \
  --bind "$WARP_BIND" \
  --cache-dir "$WARP_DATA/normal-warp-cache" \
  --verbose
```

另开一个 SSH 终端验证：

```bash
curl --socks5-hostname "$WARP_BIND" https://api.ipify.org
ss -lntp | grep 10086
```

成功标志：

```text
connection test successful
serving proxy address=127.0.0.1:10086
curl 能返回一个出口 IP
```

失败处理：

```text
如果这里失败，优先换 WARP endpoint 或先跑 scan，不进入 cfon/pool。
```

停止前台进程：

```bash
Ctrl+C
```

## 3. 旧 cfon 单进程验证

目的：

```text
这是当前最关键的一步。
Windows 上 US / SG / JP 都卡在 Psiphon handshake。
Ubuntu 如果旧 cfon 成功，说明问题更可能是 Windows 本地网络环境或时段问题。
Ubuntu 如果旧 cfon 也失败，说明问题更可能是 Psiphon server list/节点/当前网络路径问题，仍不应先改 pool。
```

先测 US：

```bash
"$WARP_PLUS" -4 \
  --endpoint "$WARP_ENDPOINT" \
  --cfon --country US \
  --test-url http://example.com/ \
  --bind "$WARP_BIND" \
  --cache-dir "$WARP_DATA/single-cfon-cache-us" \
  --egress-check \
  --egress-blacklist "$WARP_BLACKLIST" \
  --egress-max-retry 2 \
  --egress-check-interval 0 \
  --verbose
```

另开一个 SSH 终端验证：

```bash
curl --socks5-hostname "$WARP_BIND" https://api.ipify.org
ss -lntp | grep 10086
```

成功标志：

```text
connection test successful
psiphon started successfully
egress accepted
serving proxy address=127.0.0.1:10086
curl 能返回 Psiphon 出口 IP
```

失败标志：

```text
EstablishTunnelTimeout
clientlib: tunnel establishment timeout
controller.Run exited unexpectedly
unable to find acceptable egress after 2 attempts
```

如果 US 失败，再按同一命令换国家：

```bash
# JP
"$WARP_PLUS" -4 \
  --endpoint "$WARP_ENDPOINT" \
  --cfon --country JP \
  --test-url http://example.com/ \
  --bind "$WARP_BIND" \
  --cache-dir "$WARP_DATA/single-cfon-cache-jp" \
  --egress-check \
  --egress-blacklist "$WARP_BLACKLIST" \
  --egress-max-retry 2 \
  --egress-check-interval 0 \
  --verbose
```

```bash
# DE
"$WARP_PLUS" -4 \
  --endpoint "$WARP_ENDPOINT" \
  --cfon --country DE \
  --test-url http://example.com/ \
  --bind "$WARP_BIND" \
  --cache-dir "$WARP_DATA/single-cfon-cache-de" \
  --egress-check \
  --egress-blacklist "$WARP_BLACKLIST" \
  --egress-max-retry 2 \
  --egress-check-interval 0 \
  --verbose
```

## 4. pool 前台验证

前提：

```text
只有旧 cfon 单进程至少有一个国家成功后，才执行本节。
如果旧 cfon 全部失败，pool 测试没有诊断价值。
```

目的：

```text
验证多进程池能达到 1 active + 1 ready。
验证 control API 能 refresh。
验证 refresh 失败时不会影响旧 active 继续服务。
```

命令：

```bash
"$WARP_PLUS" -4 \
  --endpoint "$WARP_ENDPOINT" \
  --cfon --country US \
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
  --verbose
```

成功标志：

```text
child registry listening
started child process id=0
child egress accepted
child activated (first)
first child active
pool maintenance started
started child process id=1
child ready
serving rotate control address=127.0.0.1:9099
```

另开一个 SSH 终端验证：

```bash
curl http://127.0.0.1:9099/health
curl --socks5-hostname "$WARP_BIND" https://api.ipify.org
curl -X POST "http://$WARP_CONTROL/connectivity/refresh" \
  -H "Authorization: Bearer $WARP_TOKEN"
curl --socks5-hostname "$WARP_BIND" https://api.ipify.org
cat "$WARP_DATA/pool-cache/recent_ips.txt"
```

期望结果：

```text
/health 返回 {"status":"ok"}
refresh 返回 {"status":"ok"} 或没有 ready 时返回 {"status":"busy"}
成功 refresh 后，新连接出口 IP 变化
recent_ips.txt 记录旧 active IP
```

## 5. 进程和端口检查

目的：

```text
Ubuntu 前台通过后，必须确认没有 zombie、没有子进程残留、端口只监听 loopback。
这是进入 systemd 前的硬门槛。
```

命令：

```bash
ps -eo stat,pid,ppid,cmd | grep warp-plus | grep -v grep
ss -lntp | grep -E '10086|9099'
```

重点看：

```text
进程 STAT 不应出现 Z。
10086 和 9099 应监听 127.0.0.1，不应监听 0.0.0.0。
停止前台程序后，不应残留 child 进程。
```

停止前台程序后检查：

```bash
ps -eo stat,pid,ppid,cmd | grep warp-plus | grep -v grep
ss -lntp | grep -E '10086|9099'
```

## 6. 判断分支

### 分支 A：普通 WARP 失败

结论：

```text
endpoint 或 Ubuntu 网络基础链路不可用。
```

下一步：

```text
换 endpoint。
先不要测 cfon。
先不要测 pool。
```

### 分支 B：普通 WARP 成功，旧 cfon 失败

结论：

```text
问题仍在 Psiphon handshake，不是 pool。
```

下一步：

```text
换 country、换时间窗口、换网络环境。
必要时检查服务器出站到 s3.amazonaws.com:443 是否受限。
```

### 分支 C：旧 cfon 成功，pool 失败

结论：

```text
问题转向 pool child 参数、目录隔离、子进程环境或注册流程。
```

下一步：

```text
比较旧 cfon 与 child 传给 Psiphon 的 cache 目录和启动参数。
优先检查 child cache 是否应拆成 child_N/primary 与 child_N/psiphon。
```

### 分支 D：旧 cfon 成功，pool 也成功

结论：

```text
Ubuntu 前台链路可用。
```

下一步：

```text
连续 refresh 5-10 次。
确认无 zombie、无端口泄漏。
然后再写 systemd unit。
最后接入 S-UI / sing-box。
```

## 7. 暂不做的事

在旧 cfon 成功前，不做：

```text
不改 systemd。
不接 S-UI / sing-box。
不继续重构 pool。
不扩大并发和稳定性测试。
```

原因：

```text
当前最小阻塞点是 Psiphon handshake。
先把单进程最小路径跑通，后面的 pool、systemd、业务接入才有验证意义。
```

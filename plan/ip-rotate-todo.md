# IP rotate / pool 模式 TODO

本文档用于记录当前已经完成的分析、下一步要做的对照实验，以及后续直到 Ubuntu 代理服务器部署前的操作顺序。

## 当前已确认结论

### 1. `--test-url http://example.com/` 仍然必须保留

当前版本的 WARP 启动连通性测试使用 `HEAD` 请求，并要求 HTTP 状态码为 `200`。

因此以下地址不适合作为当前默认测试地址：

```text
http://connectivitycheck.gstatic.com/generate_204
```

原因是 `generate_204` 正常返回 `204`，会被当前代码误判为失败。

所有普通 WARP、旧 cfon、pool 模式验证命令都应显式带上：

```powershell
--test-url http://example.com/
```

最近一次 pool 测试已经带了该参数，并且日志显示 WARP 阶段成功：

```text
child_id=0 connection test successful
child_id=1 connection test successful
```

所以当前阻塞点不是 `--test-url` 缺失。

### 2. parent / child 生命周期监控已修复

已完成：

- child 启动后立即进入 `cmd.Wait()` goroutine。
- child 退出后关闭 `waitDone`，记录 exit code，并唤醒 pool。
- first child 启动阶段同时监听：注册成功、child 退出、启动超时。
- warming child 有独立启动超时。
- warming 超时从固定 `120s` 调整为动态计算：

```text
max(120s, egressMaxRetry * 90s + 30s)
```

原因是每次 Psiphon 尝试可能需要约 60-75 秒。`--egress-max-retry 2` 时，固定 120 秒会在第二次尝试尚未完成时提前 kill child。

验证结果：

```text
child_0 两次 Psiphon 尝试完整跑完后退出
child_1 两次 Psiphon 尝试完整跑完后退出
parent 未提前 kill 第二次尝试
```

### 3. 默认 Windows cache 下的 data store 报错是 ACL / 沙箱权限问题

真实错误：

```text
Unable to start psiphon: failed to open data store:
open C:\Users\orechi\AppData\Local\cache\warp-plus\child_0\ca.psiphon.PsiphonTunnel.tunnel-core\datastore\psiphon.boltdb:
Access is denied.
```

进一步验证：

```text
Codex Windows 沙箱用户对默认 LocalAppData cache 下的 datastore 目录没有写权限。
手动写入 write_probe.tmp 也会 Access is denied。
```

结论：

```text
Windows 本地 pool 测试不要使用默认 cache。
应显式指定工作区内 cache。
```

推荐本地测试参数：

```powershell
--cache-dir test-output\pool-cache
--recent-ip-file test-output\pool-cache\recent_ips.txt
```

使用工作区 cache 后，`Access is denied` 消失。

### 4. 当前新的阻塞点是 Psiphon handshake 超时

使用工作区 cache 后，child 已经能打开 data store，并进入 Psiphon handshake。

当前失败变成：

```text
clientlib: tunnel establishment timeout
controller.Run exited unexpectedly
```

因此下一步不是继续排查 `--test-url`，也不是继续排查 data store 打不开，而是做旧 cfon 与 pool child 的同条件对照。

## 下一步要分析什么

目标：判断 Psiphon handshake 超时是当前网络 / endpoint / Psiphon 节点不稳定，还是 pool child 模式引入的问题。

### 对照实验 A：旧 cfon 单进程路径

使用与 pool child 尽量一致的条件：

- 固定同一个已验证 endpoint。
- 显式指定 `--test-url http://example.com/`。
- 显式指定工作区 cache。
- 启用 `--cfon --country US`。
- 启用 `--egress-check`。
- 关闭运行期监控，避免干扰启动阶段判断。

推荐命令：

```powershell
.\test-output\warp-plus-datastore-diagnostic.exe -4 `
  --endpoint 188.114.98.7:3581 `
  --cfon --country US `
  --test-url http://example.com/ `
  --bind 127.0.0.1:11086 `
  --cache-dir test-output\single-cfon-cache `
  --egress-check `
  --egress-blacklist .\blacklist.example.txt `
  --egress-max-retry 2 `
  --egress-check-interval 0 `
  --verbose
```

成功标志：

```text
connection test successful
psiphon started successfully
egress accepted
serving proxy address=127.0.0.1:11086
```

失败标志：

```text
clientlib: tunnel establishment timeout
controller.Run exited unexpectedly
```

### 对照实验 B：pool 模式

如果旧 cfon 成功，再用同样 endpoint 和工作区 cache 跑 pool：

```powershell
.\test-output\warp-plus-datastore-diagnostic.exe -4 `
  --endpoint 188.114.98.7:3581 `
  --cfon --country US `
  --test-url http://example.com/ `
  --bind 127.0.0.1:11086 `
  --cache-dir test-output\pool-cache `
  --egress-check `
  --egress-blacklist .\blacklist.example.txt `
  --egress-max-retry 2 `
  --egress-check-interval 0 `
  --control-bind 127.0.0.1:19099 `
  --control-token test-token `
  --recent-ip-file test-output\pool-cache\recent_ips.txt `
  --recent-ip-limit 50 `
  --pool-min-ready 1 `
  --verbose
```

成功标志：

```text
first child active
pool maintenance started
started child process id=1
child ready
```

随后验证：

```powershell
curl.exe http://127.0.0.1:19099/health
curl.exe --socks5 127.0.0.1:11086 https://api.ipify.org
curl.exe -X POST http://127.0.0.1:19099/connectivity/refresh `
  -H "Authorization: Bearer test-token"
curl.exe --socks5 127.0.0.1:11086 https://api.ipify.org
```

## 下一步完成后的判断分支

### 分支 1：旧 cfon 单进程也超时

结论：

```text
当前问题更可能是 Psiphon 网络、endpoint、国家出口或本地网络环境不稳定。
pool 不是主因。
```

后续操作：

- 换一个普通 WARP 已验证 endpoint 后重测。
- 先跑普通 WARP，确认 endpoint 当前可用。
- 再跑旧 cfon。
- 必要时增加 `--egress-max-retry`，但不要先改 pool 逻辑。

### 分支 2：旧 cfon 成功，但 pool child 超时

结论：

```text
问题更可能在 child 模式参数、目录隔离、子进程环境，或 child 内重试方式。
```

后续操作：

- 比较旧 cfon 与 child 模式传给 `psiphon.StartPsiphon()` 的参数差异。
- 检查 child cache 目录是否应拆成：

```text
child_N/primary
child_N/psiphon
```

而不是让 Psiphon 直接使用：

```text
child_N
```

- 考虑调整 child 内部策略：Psiphon 一次启动失败后直接退出，让 parent 拉起新 child，避免同一 child 进程内反复触碰 Psiphon 全局状态。

### 分支 3：旧 cfon 成功，pool 也达到 `1 active + 1 ready`

结论：

```text
pool 基础启动链路恢复。
```

后续操作：

- 验证手动 refresh。
- 验证 recent IP 文件。
- 验证并发 refresh。
- 验证 active child 崩溃后的自动提升。
- 准备 Ubuntu 前台模式验证。

## 后续完整操作顺序

### 阶段 1：Windows 本地恢复 pool 基础成功

验收目标：

```text
1 active + 1 ready
/health 返回 ok
业务代理端口可用
refresh 返回 ok
refresh 后出口 IP 变化或符合 require_change 规则
```

必须使用：

```powershell
--test-url http://example.com/
--cache-dir test-output\...
```

### 阶段 2：Windows 本地稳定性验证

验收目标：

- 连续 refresh 5-10 次。
- recent IP 文件按预期更新。
- 并发 refresh 时，一个请求处理，其他请求返回 `busy`。
- child 提前退出时 parent 能感知并补充。
- active child 崩溃时，有 ready 可自动提升。
- 无端口泄漏。

### 阶段 3：Ubuntu 前台模式验证

在 Ubuntu 上先不要写 systemd，先前台运行。

核心要求：

```text
--test-url http://example.com/
--cache-dir /var/lib/warp-plus
--bind 127.0.0.1:10086
--control-bind 127.0.0.1:9099
```

检查项：

```bash
ps -eo stat,pid,ppid,cmd | grep warp-plus
ss -lntp | grep -E '10086|9099'
curl --socks5-hostname 127.0.0.1:10086 https://api.ipify.org
```

重点确认：

- 没有 zombie 进程。
- 没有 child 残留。
- 端口只监听 `127.0.0.1`。
- refresh 失败时旧 active 仍能服务。

### 阶段 4：systemd 托管

只有 Ubuntu 前台模式通过后，才进入 systemd。

systemd 参数文件中必须保留：

```bash
--test-url http://example.com/
--cache-dir /var/lib/warp-plus
--bind 127.0.0.1:10086
--control-bind 127.0.0.1:9099
```

并确认：

```bash
journalctl -u warp-plus -f
systemctl restart warp-plus
systemctl status warp-plus
```

### 阶段 5：接入 S-UI / sing-box

只有 systemd 稳定后，才把 S-UI / sing-box 的 SOCKS 出站指向：

```text
127.0.0.1:10086
```

先只给少量测试用户或测试规则走 `warp-plus-out`，不要一次性切全部流量。

## 当前 TODO

- [x] 跑旧 cfon 单进程同条件对照实验。
- [x] 根据旧 cfon 结果判断是 Psiphon 网络问题，还是 pool child 问题。
- [ ] 如果是 pool child 问题，比较旧 cfon 与 child 参数差异。
- [ ] 如果需要，拆分 child cache：`primary` 与 `psiphon` 独立目录。
- [ ] 如果需要，调整 child 内部策略：Psiphon 单次失败后退出，由 parent 重建 child。
- [ ] pool 达到 `1 active + 1 ready` 后验证 refresh。
- [ ] Windows 稳定后，再做 Ubuntu 前台验证。
- [ ] Ubuntu 前台通过后，再写 systemd 并接入 S-UI。

## 对照实验记录（2026-05-27 23:19-23:45）

### 实验 A1：旧 cfon 单进程，固定 endpoint `188.114.98.7:3581`

命令使用工作区 cache，并显式保留：

```powershell
--test-url http://example.com/
--cache-dir test-output\single-cfon-cache
--egress-max-retry 2
--egress-check-interval 0
```

结果：

```text
connection test successful
starting egress candidate attempt=1 max_retry=2
psiphon core notice type=EstablishTunnelTimeout timeout=1m0s
egress candidate handshake failed attempt=1 error="Unable to start psiphon: clientlib: tunnel establishment timeout"
starting egress candidate attempt=2 max_retry=2
egress candidate handshake failed attempt=2 error="Unable to start psiphon: controller.Run exited unexpectedly"
unable to find acceptable egress after 2 attempts
```

判断：

```text
同 endpoint 下 WARP 阶段成功，失败点在 Psiphon handshake。
旧 cfon 单进程也失败，因此当前不应优先归因到 pool child 生命周期或 relay 切换逻辑。
```

### 实验 A2：旧 cfon 单进程，历史成功 endpoint `188.114.97.246:864`

命令同 A1，仅替换 endpoint、端口和 cache：

```powershell
--endpoint 188.114.97.246:864
--bind 127.0.0.1:11087
--cache-dir test-output\single-cfon-cache-endpoint2
```

结果：

```text
connection test successful
starting egress candidate attempt=1 max_retry=2
psiphon core notice type=EstablishTunnelTimeout timeout=1m0s
egress candidate handshake failed attempt=1 error="Unable to start psiphon: clientlib: tunnel establishment timeout"
starting egress candidate attempt=2 max_retry=2
egress candidate handshake failed attempt=2 error="Unable to start psiphon: controller.Run exited unexpectedly"
unable to find acceptable egress after 2 attempts
```

判断：

```text
更换一个历史成功 endpoint 后仍失败，进一步支持当前是 Psiphon 网络、Psiphon server list/节点、US 出口或本地网络时段问题。
pool 模式不是当前主因。
```

### 下一步收敛

优先按分支 1 处理：

- 先不要改 pool 生命周期逻辑。
- `SG`、`JP` 已复测，仍然 Psiphon handshake 超时。
- 可继续换 Psiphon country，例如 `DE`，验证是否区域出口不可用。
- 可在不同时间窗口复测旧 cfon。
- 如果 Windows 旧 cfon 持续失败，直接进入 Ubuntu 前台旧 cfon 验证，判断是否是 Windows 本地网络环境问题。

### 实验 A3：旧 cfon 单进程，country `SG`

命令同 A1，保留 endpoint `188.114.98.7:3581`，仅替换：

```powershell
--country SG
--bind 127.0.0.1:11088
--cache-dir test-output\single-cfon-cache-sg
```

结果：

```text
connection test successful
starting egress candidate attempt=1 max_retry=2
psiphon core notice type=EstablishTunnelTimeout timeout=1m0s
egress candidate handshake failed attempt=1 error="Unable to start psiphon: controller.Run exited unexpectedly"
starting egress candidate attempt=2 max_retry=2
psiphon core notice type=EstablishTunnelTimeout timeout=1m0s
egress candidate handshake failed attempt=2 error="Unable to start psiphon: controller.Run exited unexpectedly"
unable to find acceptable egress after 2 attempts
```

判断：

```text
SG 与 US 表现一致：WARP 阶段可用，Psiphon handshake 不可用。
问题范围继续收敛到 Psiphon server list/节点、当前本地网络环境或 Windows 时段问题。
```

### 实验 A4：旧 cfon 单进程，country `JP`

命令同 A1，保留 endpoint `188.114.98.7:3581`，仅替换：

```powershell
--country JP
--bind 127.0.0.1:11089
--cache-dir test-output\single-cfon-cache-jp
```

结果：

```text
connection test successful
starting egress candidate attempt=1 max_retry=2
psiphon core notice type=EstablishTunnelTimeout timeout=1m0s
egress candidate handshake failed attempt=1 error="Unable to start psiphon: clientlib: tunnel establishment timeout"
starting egress candidate attempt=2 max_retry=2
psiphon core notice type=EstablishTunnelTimeout timeout=1m0s
egress candidate handshake failed attempt=2 error="Unable to start psiphon: controller.Run exited unexpectedly"
unable to find acceptable egress after 2 attempts
```

判断：

```text
JP 与 US、SG 表现一致：WARP 阶段可用，Psiphon handshake 不可用。
当前更像是 Psiphon 网络/server list/本地 Windows 网络环境问题，而不是指定国家出口单点不可用。
```

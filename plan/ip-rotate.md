# warp-plus 手动触发 IP 轮换开发计划（v2 — 多进程池架构）

## 旧方案回顾与失败原因

v1 方案尝试在单一进程内关闭旧 Psiphon tunnel 再启动新的：

```
Client → POST /connectivity/refresh → rotate()
  → old.tunnel.Close()
  → startAccepted() → StartPsiphon() → StartTunnel() → psiphon.OpenDataStore()
```

**失败根因：** Psiphon 库的 `OpenDataStore` / `CloseDataStore` / `SetNoticeWriter` 均为进程级全局单例，不支持同一进程内 Close→Open 循环。旧 tunnel 的 `Close()` 只等 5 秒就让 controller 停機（实际需要更久），在 controller 还在运行时强行 `CloseDataStore()` 导致 SQLite 数据库损坏。新 tunnel 的 `OpenDataStore()` 面对损坏的 data store，server entries 丢失，永远建连失败（硬等 75 秒超时）。

**更深层的问题：** 即便修复了关闭序列，Psiphon 建连本身就需 30-75 秒。每隔几分钟要切换一次，每次等半分钟以上不现实。

## v2 方案：多进程池

核心思路：**既然 Psiphon 全局状态限制了一个进程只能有一个 tunnel，那就用多个子进程，每个子进程独立跑一套完整的 Psiphon+WARP 栈。主进程只负责 tcpRelay 在子进程之间切换上游端口。**

切换 relay 上游只是修改一个内存字符串——毫秒级。

```
                       ┌─────────────────────────────────────────┐
                       │          warp-plus (主进程 / parent)      │
                       │                                          │
S-UI ────────────────→ │  tcpRelay       :10086                   │
                       │  rotate_control :9099                    │
                       │  child_registry :9098 (内网)              │
                       │         ↓                                │
                       │    setUpstream() —— 切换上游端口（毫秒级）  │
                       └────┬────────────┬────────────┬───────────┘
                            │            │            │
                  socks5:// │   socks5://│   socks5://│
                127.0.0.1:  │ 127.0.0.1: │ 127.0.0.1: │
                   11001    │    11002   │    11003   │
                            │            │            │
                 ┌──────────┴─┐ ┌────────┴─┐ ┌────────┴──┐
                 │ warp-plus  │ │warp-plus │ │ warp-plus  │
                 │ (子进程#0)  │ │(子进程#1) │ │ (子进程#2) │
                 │            │ │          │ │  (预热中)   │
                 │ WARP       │ │ WARP     │ │ WARP       │
                 │ + Psiphon  │ │ +Psiphon │ │ + Psiphon  │
                 │ + egress✓  │ │ +egress✓ │ │ (handshake)│
                 │            │ │          │ │            │
                 │ 出口IP:     │ │ 出口IP:   │ │            │
                 │ 1.2.3.4    │ │ 5.6.7.8  │ │            │
                 └────────────┘ └──────────┘ └────────────┘
                      ↑ 当前服务     ↑ 已就绪        ↑ 准备中
```

## 进程角色

### 主进程（parent）

- 解析 CLI 参数，加载 blacklist、recent IP file
- **不启动任何 Psiphon/WARP tunnel**
- 启动 tcpRelay，监听 `--bind` 端口（例如 `127.0.0.1:10086`）
- 启动 control HTTP server，监听 `--control-bind` 端口（例如 `127.0.0.1:9099`）
- 启动 child registry HTTP server，监听 `127.0.0.1:0`（随机端口，仅内网）
- 管理子进程池：启动、监控、回收
- `POST /connectivity/refresh` 到来时，从池中选一个已就绪的子进程，切换 relay 上游
- 切换后 kill 旧子进程，启动新子进程补充池
- 定期 egress 监控：检查当前出口是否仍然合格

### 子进程（child）

- 通过 `--child` 标志识别
- 从 CLI 参数接收完整的 WARP + Psiphon + egresscheck 配置
- 使用独立的 data 目录：`<cachedir>/child_<N>/`
- 执行完整建连流程：WARP → Psiphon → egresscheck（blacklist + score）
- 建连成功且 egress 检查通过后，向主进程的 registry 上报：`{port, ip, child_id, token}`
- 上报成功后阻塞等待 `ctx.Done()`（主进程 kill 时触发）
- 被 kill 时清理自身 tunnel

## 子进程注册协议

主进程启动子进程时传递随机 token 和 parent registry 地址：

```powershell
.\warp-plus.exe --child --child-id 0 --child-token <random> --parent-addr 127.0.0.1:XXXXX `
  --cfon --country US --egress-check ... (其余配置与主进程相同)
```

子进程就绪后向主进程发送注册请求：

```http
POST /child/register
Content-Type: application/json

{
  "child_id": 0,
  "token": "<random>",
  "port": 11001,
  "ip": "1.2.3.4"
}
```

主进程响应：

```json
{"status": "ok"}
```

或：

```json
{"status": "rejected", "reason": "..."}
```

## 池管理逻辑

### 初始启动

```
1. 启动子进程 #0
2. 等待子进程 #0 注册就绪 → 设为 active，relay 指向 #0 的端口
3. 启动子进程 #1（后台，不阻塞）
4. 子进程 #1 注册就绪 → 加入 ready 池
5. 服务就绪：1 active + 1 ready
```

### Rotate 流程

```
POST /connectivity/refresh
  ├── 无 ready 子进程 → {"status": "busy"}
  ├── 有 ready 子进程
  │   ├── 检查 ready 的 IP ≠ 当前 active IP（如果 require_change）
  │   ├── 检查 ready 的 IP 不在 recent IPs 中
  │   ├── 均通过：
  │   │   ├── relay.setUpstream(ready.port)   ← 毫秒级切换
  │   │   ├── 旧 active 子进程 kill
  │   │   ├── ready → active
  │   │   ├── 旧 active IP 写入 recent IPs
  │   │   ├── 启动新子进程补充 ready 池
  │   │   └── {"status": "ok"}
  │   └── 未通过（IP 冲突）：
  │       ├── kill 该 ready 子进程
  │       ├── 尝试下一个 ready 或启动新子进程
  │       └── {"status": "no_acceptable_egress"}（如果所有 ready 都不合格）
```

### 后台维护

```
- ready 池数量 < minReady (默认 1) → 启动新子进程补充
- 子进程意外退出 → 从池中移除，启动替代
- 子进程启动超时 (默认 90s) → kill，重试
- 定期 egress 检查当前 active：
  - 合格 → 无操作
  - 不合格 → 触发 rotate
```

## CLI 参数变更

### 新增参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `--child` | 启用子进程模式（布尔标志） | `false` |
| `--child-id` | 子进程 ID（由主进程分配） | `0` |
| `--child-token` | 子进程注册 token（由主进程生成） | 空 |
| `--parent-addr` | 主进程 registry 地址 | 空 |
| `--pool-min-ready` | 最小就绪子进程数 | `1` |

### 保留参数

所有现有参数保持不变，子进程模式下仍接受完整的 `--cfon`、`--egress-check`、`--country` 等参数。

### 启动示例

主进程：

```powershell
.\warp-plus.exe -4 --scan --cfon --country US `
  --bind 127.0.0.1:10086 `
  --egress-check `
  --egress-blacklist .\blacklist.example.txt `
  --egress-max-retry 3 `
  --control-bind 127.0.0.1:9099 `
  --control-token test-token `
  --recent-ip-file .\recent_ips.txt `
  --recent-ip-limit 50 `
  --pool-min-ready 1
```

触发切换：

```powershell
curl.exe -X POST http://127.0.0.1:9099/connectivity/refresh `
  -H "Authorization: Bearer test-token"
```

## 数据目录隔离

每个子进程使用独立的缓存子目录，避免 data store 冲突：

```
<cachedir>/
├── primary/              # WARP identity（共享）
├── child_0/
│   └── psiphon/          # Psiphon data store（独立）
├── child_1/
│   └── psiphon/
├── child_2/
│   └── psiphon/
└── recent_ips.txt        # 共享 recent IP 文件
```

子进程独立 data store 的关键好处：
- 旧子进程被 kill 时，其 data store 由操作系统自然回收（进程退出即释放文件锁）
- 不再有 `CloseDataStore → OpenDataStore` 循环，从根本上消除数据库损坏问题
- 每个子进程启动时 data store 是全新的，server entries 从 embedded 导入

## 子进程超时与重试

```
子进程启动 → 90s 超时
  ├── 90s 内注册就绪 → 加入 pool
  └── 超时 → 主进程 kill 子进程，重试（最多同 --egress-max-retry）

子进程意外退出：
  ├── 如果在 ready 池中 → 移除，启动替代
  └── 如果是 active → 立即从 ready 池选一个切换（fallback rotate）
```

## 代码改造点

### `app/child.go`（新增）

子进程模式入口。职责：

- 解析 `--child` 标志，执行子进程逻辑
- 启动 WARP + Psiphon + egresscheck（复用现有 `startAccepted`）
- 向主进程 registry 发送注册请求
- 阻塞等待 context 取消

### `app/pool.go`（新增）

子进程池管理器。职责：

- 维护 active / ready / warming 子进程列表
- 启动子进程（`os/exec`）
- 接收子进程注册（HTTP endpoint `/child/register`）
- 提供 `acquireReady()` 方法给 rotate 使用
- 后台维护池大小

### `app/app.go`（修改）

- 新增 `runWarpWithPool()` 函数，替代原有 `runWarpWithCheckedPsiphon` 的大部分逻辑
- `WarpOptions` 增加 `PoolMinReady` 字段
- `RotateControlOptions` 增加 `ParentAddr` 字段（子进程使用）

### `app/rotate_control.go`（修改）

- `/health` 保留
- 原 rotate endpoint 改为调用 pool 的 `acquireReady()` + relay 切换
- 新增 `/child/register` endpoint（仅监听在内网随机端口）

### `cmd/warp-plus/rootcmd.go`（修改）

- 新增 `--child`、`--child-id`、`--child-token`、`--parent-addr`、`--pool-min-ready` 参数
- 子进程模式下跳过 relay 和 control server 启动，直接执行 child 逻辑

### `psiphon/p.go`（可能需要微调）

- 如果子进程各自使用独立 data 目录，不需要修改 `Close()` 的等待时间问题（进程被 kill 时 OS 清理）
- 可选改进：`Tunnel.Close()` 的等待时间从 5s 增加到 30s，作为防御性措施

## 状态码

| 场景 | HTTP | JSON |
|------|------|------|
| 刷新成功 | 200 | `{"status":"ok"}` |
| 无就绪子进程可用 | 503 | `{"status":"busy"}` |
| token 错误 | 401 | `{"status":"unauthorized"}` |
| method 错误 | 405 | `{"status":"method_not_allowed"}` |
| 所有就绪子进程 IP 冲突 | 503 | `{"status":"no_acceptable_egress"}` |
| 内部错误 | 500 | `{"status":"refresh_failed"}` |

## 生命周期与泄漏防护

- 每个子进程由 `context.Context` 控制生命周期，主进程取消 context 时子进程被 kill
- 主进程退出时，遍历所有子进程发送 `os.Interrupt`，等待 5s 后强制 `os.Kill`
- 子进程使用 `127.0.0.1:0` 分配随机端口，端口在进程退出后由 OS 回收
- 不再有"同时存在两个 Psiphon controller"的场景，从根本上消除 data store 竞争

## 与定时检查的关系

定时 egress 检查仍存在，但逻辑简化：
- 定时检查当前 active 子进程的出口 IP 是否仍然合格
- 不合格时触发 rotate（复用相同的池切换逻辑）
- 控制接口的刷新和定时检查共用同一个 pool manager 和 relay

## Windows 本地验证记录（2026-05-27）

### 验证结论

当前多进程池模式已经完成一次端到端手动验证：

- 普通 WARP 模式可用
- 旧 `--cfon + --egress-check` 路径可用
- pool 模式可启动 active child 和 ready child
- `/health` 正常返回
- `POST /connectivity/refresh` 返回 `{"status":"ok"}`
- rotate 后业务代理出口 IP 发生变化
- `recent_ips.txt` 在成功切换后更新

本次成功观测：

```powershell
curl.exe --socks5 127.0.0.1:10086 https://api.ipify.org
# 104.28.208.133 / 45.79.111.188 / 50.116.50.12 等，不同阶段出口不同

curl.exe http://127.0.0.1:9099/health
# {"status":"ok"}

curl.exe -X POST http://127.0.0.1:9099/connectivity/refresh `
  -H "Authorization: Bearer test-token"
# {"status":"ok"}

Get-Content .\recent_ips.txt
# recent IP 文件成功追加/滚动，例如包含 50.116.50.12
```

### 成功测试命令

先验证普通 WARP。关键是显式指定 `--test-url http://example.com/`：

```powershell
.\warp-plus-rotate-test.exe -4 --scan `
  --test-url http://example.com/ `
  --bind 127.0.0.1:10086 `
  --verbose
```

成功标志：

```text
connection test successful
serving proxy address=127.0.0.1:10086
```

普通 WARP 成功后，从日志中的 `using warp endpoints` 或 `ping success` 选择一个已验证 endpoint，固定 endpoint 测旧 cfon 路径，避免每次被 scan 阶段的不稳定性干扰：

```powershell
.\warp-plus-rotate-test.exe -4 --endpoint 188.114.98.7:3581 --cfon --country US `
  --test-url http://example.com/ `
  --bind 127.0.0.1:10086 `
  --egress-check `
  --egress-blacklist .\blacklist.example.txt `
  --egress-max-retry 3 `
  --egress-check-interval 0 `
  --verbose
```

旧 cfon 路径成功后，再测 pool 模式：

```powershell
.\warp-plus-rotate-test.exe -4 --endpoint 188.114.98.7:3581 --cfon --country US `
  --test-url http://example.com/ `
  --bind 127.0.0.1:10086 `
  --egress-check `
  --egress-blacklist .\blacklist.example.txt `
  --egress-max-retry 3 `
  --egress-check-interval 0 `
  --control-bind 127.0.0.1:9099 `
  --control-token test-token `
  --recent-ip-file .\recent_ips.txt `
  --recent-ip-limit 50 `
  --pool-min-ready 1 `
  --verbose
```

另开 PowerShell 执行：

```powershell
curl.exe http://127.0.0.1:9099/health
curl.exe --socks5 127.0.0.1:10086 https://api.ipify.org

curl.exe -X POST http://127.0.0.1:9099/connectivity/refresh `
  -H "Authorization: Bearer test-token"

curl.exe --socks5 127.0.0.1:10086 https://api.ipify.org
Get-Content .\recent_ips.txt
```

### 失败经验与归因

1. 不要用 `http://connectivitycheck.gstatic.com/generate_204` 作为当前版本的 `--test-url`。

   当前 `usermodeTunTest()` 使用 `HEAD` 请求，并且要求状态码必须是 `200`。`generate_204` 的正常返回是 `204`，会被判定为失败，即使网络实际可通。

2. 默认测试地址和部分 Cloudflare trace 地址在本地环境中可能导致：

   ```text
   connection test failed
   context deadline exceeded
   ```

   这类失败发生在启动代理前，不代表 pool 或 Psiphon 已经失败。必须先看到 `connection test successful` 和 `serving proxy`，才算普通 WARP 已经可用。

3. `--scan` 在本地网络中波动较大。

   现象包括：

   ```text
   ping error ... i/o timeout
   invalid handshake response length 16 bytes
   user canceled the operation
   ```

   这些错误说明 scan 阶段没有稳定产出足够 endpoint。排查 pool 时建议固定一个普通 WARP 已验证的 endpoint，例如本次成功使用的 `188.114.98.7:3581`。

4. 子进程未注册时，旧实现会等到 first child 超时。

   现象：

   ```text
   error: child: wireguard setup failed for all attempts
   pool: timeout waiting for first child to become ready
   ```

   这次根因是子进程里的 WARP 连通性测试失败；但也暴露了 pool 生命周期管理问题：first child 失败退出后，parent 没有立即感知并重试，而是等待 120 秒超时。

   2026-05-27 本轮已修复：`cmd.Start()` 成功后立即启动 `cmd.Wait()` goroutine。child 退出后会关闭 `waitDone`、记录 exit code、唤醒 pool。first child 启动阶段现在同时监听注册成功、子进程退出和启动超时。

5. `relay accept failed ... use of closed network connection` 多数是主进程退出时关闭 listener 的伴随噪声。

   这条日志不是启动失败根因。真正需要优先看的是它之前的 `connection test failed`、`wireguard setup failed`、`child register` 或 `pool timeout`。

   2026-05-27 本轮已修复：relay accept 遇到 `net.ErrClosed` 时直接退出，不再刷关闭 listener 的噪声日志。

### 生命周期修复记录（2026-05-27）

本轮目标：按 Ubuntu/Linux 生产语义修复 parent 对 child 的生命周期监控，同时先在 Windows 开发环境做轻量实测。

代码层面已完成：

- `app/pool.go`：每个 child 启动后立即进入 `cmd.Wait()` goroutine，避免 Linux 上 child 退出后无人 wait 造成 zombie。
- `app/pool.go`：first child 启动阶段不再只轮询 `activeID`，而是同时监听注册成功、`waitDone`、启动超时。
- `app/pool.go`：warming child 增加独立启动超时；超时后 kill 并唤醒 pool 维护逻辑。
- `app/pool.go`：child 退出改成事件驱动处理；active child 退出时优先提升 ready child，没有 ready 时明确记录降级错误。
- `app/app.go`：pool rotate 增加互斥保护，并发请求中第二个稳定返回 `busy`。
- `app/app.go` / `app/pool.go`：rotate 顺序调整为先切 relay upstream，再 kill 旧 active child。
- `app/app.go`：成功 rotate 后记录旧 active IP 到 recent IP 文件，避免马上切回旧出口。
- `app/relay.go`：listener 正常关闭时不再输出 `use of closed network connection` 噪声。

本轮验证命令：

```powershell
$env:GOTOOLCHAIN='go1.24.3'
$env:GOCACHE='C:\My_project\warp-plus\.gocache'
$env:GOMODCACHE='C:\My_project\warp-plus\.gomodcache'

go test ./app
go build -o test-output\warp-plus-lifecycle-test.exe .\cmd\warp-plus
.\test-output\warp-plus-lifecycle-test.exe --help
```

验证结果：

```text
go test ./app                      通过
go build ./cmd/warp-plus           通过
--help 参数解析                    通过
```

Windows 实际 pool 启动验证使用端口 `11086` / `19099`，避免占用原 `10086`：

```powershell
.\test-output\warp-plus-lifecycle-test.exe -4 --endpoint 188.114.98.7:3581 --cfon --country US `
  --test-url http://example.com/ `
  --bind 127.0.0.1:11086 `
  --egress-check `
  --egress-blacklist .\blacklist.example.txt `
  --egress-max-retry 2 `
  --egress-check-interval 0 `
  --control-bind 127.0.0.1:19099 `
  --control-token test-token `
  --recent-ip-file .\recent_ips.txt `
  --recent-ip-limit 50 `
  --pool-min-ready 1 `
  --verbose
```

本轮实测结论：

```text
child_id=0 connection test successful
child_id=0 child psiphon handshake failed: failed to open data store
child process exited state=warming exit_code=1
first child failed ... pool: first child exited before registration
started child process id=1
child_id=1 connection test successful
child_id=1 child psiphon handshake failed: failed to open data store
child process exited state=warming exit_code=1
pool: start: pool: first child exited before registration
```

这说明生命周期修复生效：child 提前失败后，parent 已能立即感知、记录退出、重试下一 child，不再等待完整 120 秒超时。第二个 child 也失败后，parent 明确退出并清理 pool。

本轮新暴露的阻塞点：

```text
Unable to start psiphon: failed to open data store
```

这是 child 内部 Psiphon data store 初始化问题。它已经不是 parent 生命周期卡死问题，需要下一轮单独排查 child cache/data store 目录、Psiphon `OpenDataStore` 路径、并发打开限制或残留损坏文件。

全量测试说明：

```text
go test ./... 未作为本轮通过标准。
```

原因是全量测试仍受项目既有问题影响，包括：

- `ipscanner/engine` 的 slog vet 报错。
- `wireguard/tun` Windows 测试符号缺失。
- WireGuard 设备测试 `TestTwoDevicePing` 在当前 Windows 环境失败。

这些失败点不在本轮 pool 生命周期改动范围内。

### Data store 排查记录（2026-05-27）

本轮按顺序先增强错误诊断，再复测 pool：

- `psiphon.OpenDataStore(config)` 失败时保留真实底层错误，不再只返回 `failed to open data store`。
- Psiphon 启动前打印 `DataRootDirectory`、`MigrateDataStoreDirectory`、`MigrateRemoteServerListDownloadFilename`。

默认 Windows cache 复测得到真实错误：

```text
Unable to start psiphon: failed to open data store:
psiphon.openDataStore#155:
psiphon.tryDatastoreOpenDB#146:
open C:\Users\orechi\AppData\Local\cache\warp-plus\child_0\ca.psiphon.PsiphonTunnel.tunnel-core\datastore\psiphon.boltdb:
Access is denied.
```

进一步检查发现，当前 Codex Windows 沙箱用户对默认 LocalAppData cache 下的 Psiphon `datastore` 目录没有写权限。手动写入探针文件也会失败：

```text
Set-Content ... datastore\write_probe.tmp
Access to the path ... is denied.
```

因此，Windows 本地验证时不要使用默认 cache。应显式指定工作区内 cache：

```powershell
--cache-dir test-output\pool-cache
--recent-ip-file test-output\pool-cache\recent_ips.txt
```

使用工作区 cache 后，`Access is denied` 消失，child 可以打开 data store 并进入 Psiphon handshake。新的失败变成 Psiphon 建连超时：

```text
child_id=0 connection test successful
child_id=0 starting handshake data_root=test-output\pool-cache\child_0
child psiphon handshake failed attempt=1 error="Unable to start psiphon: clientlib: tunnel establishment timeout"
child psiphon handshake failed attempt=2 error="Unable to start psiphon: controller.Run exited unexpectedly"
first child failed ... pool: first child exited before registration
```

这说明：

- data store 问题在 Windows 本地主要是默认 cache ACL / 沙箱权限问题。
- 使用工作区 cache 后，当前阻塞点已转为 Psiphon 节点建连稳定性，而不是 data store 打不开。
- Ubuntu 部署时使用 `/var/lib/warp-plus` 且归 `warp-plus` 用户所有，理论上不应遇到这个 Windows ACL 问题，但仍需前台实测确认。

本轮还修复了一个新暴露的超时问题：

```text
旧逻辑：warming child 固定 120s 超时
问题：--egress-max-retry 2 时，第二次 Psiphon 尝试还没结束就会被 parent kill
新逻辑：child startup timeout = max(120s, egressMaxRetry * 90s + 30s)
```

复测结果：

```text
child_0 两次 Psiphon 尝试完整跑完后退出
child_1 两次 Psiphon 尝试完整跑完后退出
parent 未提前 kill 第二次尝试
parent 最终明确返回：pool: first child exited before registration
```

### 当前待修问题

- Windows 本地验证时固定使用工作区 cache，避免默认 LocalAppData cache 的沙箱 ACL 问题。
- 排查工作区 cache 下 Psiphon handshake 超时 / `controller.Run exited unexpectedly` 的原因。
- 在 Psiphon handshake 可稳定成功后，重新验证 pool 能达到 `1 active + 1 ready`。
- 验证并发 rotate：一个请求成功或进入处理，其他请求稳定返回 `busy`。
- 验证 active child 崩溃后，有 ready 时自动提升；无 ready 时业务进入明确 degraded 状态并补充 warming child。
- 在 Ubuntu 服务器前台模式下复测生命周期，重点确认无 zombie 进程、无端口泄漏，再进入 systemd 托管。

## 第一版验收标准

1. S-UI 仍然只指向 `127.0.0.1:10086`，无需修改
2. `POST /connectivity/refresh` 成功返回 `{"status":"ok"}`
3. 刷新后新业务连接走新出口 IP（relay 切换后立即生效）
4. 切换速度 < 1 秒（仅 relay 内存操作，不含子进程预热时间）
5. 新出口满足 `--country`、黑名单、评分和 recent IP 排除规则
6. 刷新失败时业务端口继续可用（旧子进程仍在服务）
7. 并发刷新请求不会同时启动多个重复切换
8. recent IP 文件在成功切换后更新
9. 多次刷新后没有僵尸子进程或端口泄漏
10. 子进程意外退出时主进程自动补充，不影响业务

## 后续可选增强

- 增加 `GET /connectivity/status`，返回当前池状态（active 数、ready 数、当前出口 IP 摘要）
- 支持 `--pool-min-ready 2` 等更高冗余配置
- 增加子进程预热超时后的告警日志
- 支持不同子进程使用不同 WARP identity 或 Psiphon country（高级流量分流）
- 增加池指标 metrics（prometheus 格式）

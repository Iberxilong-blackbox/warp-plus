# Psiphon 内部候选 IP 与出口 IP 映射调研计划

本文档记录 Ubuntu 前台验证后的新问题、当前现状、Psiphon 内部调研结论，以及下一步开发计划。

核心目标：

```text
先确认 Psiphon 内部选中的 ServerEntry.IpAddress 是否等同于最终 egress IP。
只有确认二者稳定一致或存在可靠映射后，才决定是否把 recent IP 筛选前移到 Psiphon 内部候选选择阶段。
```

## 1. 背景与当前现状

Ubuntu 服务器前台验证已经确认：

```text
普通 WARP 成功。
旧 cfon 单进程成功。
pool 父进程成功启动。
control /health 返回 {"status":"ok"}。
POST /connectivity/refresh 可以返回 {"status":"ok"} 并切换出口。
10086 和 9099 只监听 127.0.0.1。
父进程 + child 进程结构正常，未观察到 zombie。
```

当前暴露的新问题是：

```text
连续 refresh 时，部分请求返回 {"status":"busy"} 或 {"status":"no_acceptable_egress"}。
```

已观察到的日志包括：

```text
ready child has same IP as active, discarding
```

以及 `recent_ips.txt` 中已记录少量最近使用过的出口 IP，例如：

```text
66.175.234.58
74.208.212.211
107.170.226.114
74.208.85.20
```

当前判断：

```text
pool / control 主链路已经跑通。
问题不在参数解析、control API、端口监听或 child 生命周期。
问题集中在“Psiphon 选出来的 child 出口 IP 是否满足 same/recent 筛选要求”。
```

## 2. 当前筛选链路

当前实现中的筛选是后置的：

```text
child 启动
-> Psiphon 内部选择 server entry / protocol / path
-> Psiphon handshake 成功
-> child 开本地 SOCKS 端口
-> egresscheck 通过 SOCKS 请求 api.ipify.org
-> 得到最终出口 IP
-> child 注册到 parent
-> parent refresh 时再检查 same IP / recent IP
```

这会导致一个实际使用问题：

```text
ready child 看似已经准备好。
但 refresh 时才发现它的出口 IP 与当前 active 相同，或命中 recent_ips。
于是该 ready child 被丢弃，本次 refresh 返回 no_acceptable_egress。
```

## 3. Psiphon 内部调研结论

当前项目依赖的 Psiphon SDK：

```text
github.com/Psiphon-Labs/psiphon-tunnel-core
v1.0.11-0.20251029164303-aa4d266ae982
```

Psiphon 内部确实维护 `ServerEntryIterator`。

相关源码位置：

```text
psiphon/dataStore.go
psiphon/common/protocol/serverEntry.go
psiphon/controller.go
psiphon/dialParameters.go
```

`ServerEntry` 中包含候选服务器信息：

```go
type ServerEntry struct {
    IpAddress  string `json:"ipAddress,omitempty"`
    Region     string `json:"region,omitempty"`
    ProviderID string `json:"providerID,omitempty"`
}
```

`ServerEntryIterator` 的核心行为：

```text
NewServerEntryIterator(config)
-> 从 datastoreServerEntriesBucket 读取 server entry ID
-> 按 affinity / replay / shuffle 排序
-> Next() 逐个加载 ServerEntry
-> 按 EgressRegion 过滤
-> 返回候选给 establishCandidateGenerator
-> establish worker 尝试建立 tunnel
```

也就是说：

```text
Psiphon 内部在候选选择阶段能拿到 ServerEntry.IpAddress。
```

但当前尚未确认：

```text
ServerEntry.IpAddress 是否等于 api.ipify.org 看到的最终出口 IP。
```

这两个 IP 的语义不同：

```text
ServerEntry.IpAddress = Psiphon 准备连接的服务器 IP。
api.ipify.org 返回的 IP = 通过已建立 tunnel 访问公网时看到的真实出口 IP。
```

在普通直连协议下二者可能一致；在 fronting、provider NAT、不同协议路径下，二者可能不一致。

## 4. 待验证的核心假设

本轮不要直接改筛选策略，先验证以下假设：

```text
H1: 对当前 cfon US 场景，ServerEntry.IpAddress 与 egress IP 稳定一致。
H2: 二者不完全一致，但存在稳定映射关系。
H3: 二者无稳定关系，只能继续以后置 egresscheck 为准。
```

不同结论对应不同实现路线：

```text
如果 H1 成立：
可以考虑在 Psiphon 候选阶段按 ServerEntry.IpAddress 过滤 recent IP。

如果 H2 成立：
需要维护 server entry -> egress IP 映射表，再进行候选阶段优化。

如果 H3 成立：
不应把 recent_ips.txt 直接用于 Psiphon 内部候选过滤，只能优化 parent/child 边界。
```

## 5. 下一步开发计划

### 阶段 A：增加诊断日志

目标：

```text
每个 child 成功建立 Psiphon tunnel 后，记录 Psiphon 选中的 ServerEntry 信息和最终 egress IP。
```

需要新增或暴露的信息：

```text
server_entry_ip
server_entry_region
server_entry_provider_id
server_entry_diagnostic_id
egress_ip
child_id
attempt
protocol（如可低风险获得）
```

期望日志示例：

```text
child psiphon selected server child_id=3 server_entry_ip=1.2.3.4 region=US provider_id=xxx diagnostic_id=abcd1234
child egress accepted child_id=3 attempt=1 server_entry_ip=1.2.3.4 egress_ip=1.2.3.4 port=16823
```

实现原则：

```text
KISS: 只增加观测日志，不改变筛选行为。
YAGNI: 不先实现内部过滤、不新增复杂配置。
DRY: 复用现有 child egress accepted 日志上下文，避免重复查询出口 IP。
SRP: Psiphon wrapper 负责暴露已连接 server 信息，child 负责把它和 egresscheck 结果拼到日志里。
```

### 阶段 B：批量采样

在 Ubuntu 服务器上运行 pool 模式，保持实际参数：

```text
--recent-ip-limit 50
--pool-min-ready 1 或 2
--egress-max-retry 2
--country US
```

采样目标：

```text
至少收集 30-50 次 child egress accepted 样本。
记录 ServerEntry.IpAddress 与 egress IP 是否一致。
记录 same/recent 拒绝时对应的 ServerEntry.IpAddress。
```

采样后统计：

```text
server_entry_ip == egress_ip 的比例
一个 server_entry_ip 是否会对应多个 egress_ip
一个 egress_ip 是否会对应多个 server_entry_ip
same/recent 拒绝是否可以被 server_entry_ip 提前预测
```

### 阶段 C：根据结果选择实现路线

#### 路线 1：修改 Psiphon `ServerEntryIterator.Next()`

适用条件：

```text
ServerEntry.IpAddress 与 egress IP 高度一致。
```

思路：

```text
fork 或 replace psiphon-tunnel-core。
给 Config 增加排除列表或 callback。
在 ServerEntryIterator.Next() 返回候选前跳过 recent IP。
```

优点：

```text
最早过滤，避免无效 handshake。
```

风险：

```text
需要维护 Psiphon SDK patch。
如果 ServerEntry.IpAddress 不等于真实出口 IP，会误筛或漏筛。
```

#### 路线 2：修改 `establishCandidateGenerator()`

适用条件：

```text
希望减少对 iterator 的侵入，但仍在 handshake 前过滤。
```

思路：

```text
候选从 iterator 出来后，进入 worker 前检查 ServerEntry.IpAddress。
命中 recent / active 时跳过该 candidate。
```

优点：

```text
改动点比 iterator 更靠近建立流程，便于记录 skip 原因。
```

风险：

```text
仍然需要维护 Psiphon SDK patch。
仍然依赖 ServerEntry.IpAddress 与真实 egress IP 的关系。
```

#### 路线 3：不改 Psiphon 内部，前移 parent 注册校验

适用条件：

```text
ServerEntry.IpAddress 与 egress IP 不稳定，无法用于预筛。
```

思路：

```text
child 注册到 parent 时，parent 立即检查 same/recent。
不合格则拒绝注册并让 pool 继续补 child。
refresh 只从已合格 ready child 中切换。
```

优点：

```text
不侵入 Psiphon SDK。
以真实 egress IP 为准，判断可靠。
```

缺点：

```text
仍然要等 child handshake 和 egresscheck 后才能知道是否合格。
无法节省无效 handshake 时间。
```

## 6. 当前暂不做

在确认 ServerEntry IP 与 egress IP 的关系前，不做：

```text
不直接把 recent_ips.txt 塞进 Psiphon 内部过滤。
不 fork Psiphon SDK。
不改变 refresh 返回语义。
不降低 recent-ip-limit 来“制造成功率”。
```

原因：

```text
当前要测试实际使用场景。
如果 ServerEntry.IpAddress 与真实出口 IP 不一致，提前过滤会引入错误行为。
```

## 7. 验收标准

阶段 A 验收：

```text
日志中能同时看到 ServerEntry.IpAddress 和 egress IP。
不改变现有普通 WARP、旧 cfon、pool refresh 行为。
```

阶段 B 验收：

```text
收集足够样本后，能明确判断 H1 / H2 / H3。
```

阶段 C 验收：

```text
基于样本数据选择路线 1 / 2 / 3。
选择理由清晰，并能说明对 KISS、YAGNI、DRY、SOLID 的影响。
```

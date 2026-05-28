
```mermaid
graph TD
    %% 定义样式
    classDef client fill:#d4edda,stroke:#28a745,stroke-width:2px
    classDef gateway fill:#cce5ff,stroke:#007bff,stroke-width:2px
    classDef logic fill:#e2e3e5,stroke:#6c757d,stroke-width:1px,stroke-dasharray: 5 5
    classDef node fill:#fff3cd,stroke:#ffc107,stroke-width:2px
    classDef target fill:#f8d7da,stroke:#dc3545,stroke-width:2px

    %% 业务层
    subgraph 业务代码层 [1. 业务逻辑层 - 携带账号身份发起请求]
        AccA["账号 A 业务逻辑<br>(设置代理代理账号: user-acc_A)"]:::client
        AccB["账号 B 业务逻辑<br>(设置代理代理账号: user-acc_B)"]:::client
    end

    %% 网关层
    subgraph 网关层 [2. 本地中心网关 - 唯一暴露的代理端口 127.0.0.1:10086]
        Gateway{"中心路由网关<br>监听 10086 端口"}:::gateway
        
        Parse["① 解析 Proxy-Authorization<br>提取出 acc_A / acc_B"]:::logic
        Hash["② 一致性哈希计算<br>Hash(Account_ID) % 节点总数"]:::logic
        Dispatcher["③ 流量分发 (转发至对应的底层端口)"]:::logic

        Gateway --> Parse --> Hash --> Dispatcher
    end

    %% 节点池层
    subgraph 节点池层 [3. 代理节点池 - 多个独立运行的 WARP 隧道]
        Node1["WARP 实例 1 (端口:10001)<br>对应出口公网 IP: 8.8.8.1"]:::node
        Node2["WARP 实例 2 (端口:10002)<br>对应出口公网 IP: 8.8.8.2"]:::node
        NodeN["WARP 实例 N (端口:1000N)<br>对应出口公网 IP: 8.8.8.N"]:::node
    end

    %% 目标端
    Target(("OpenAI / 目标风控系统<br>基于IP进行画像分析")):::target

    %% 连接关系
    AccA -- "HTTP 代理请求\nhttp://user-acc_A:pass@127.0.0.1:10086" --> Gateway
    AccB -- "HTTP 代理请求\nhttp://user-acc_B:pass@127.0.0.1:10086" --> Gateway

    Dispatcher -- "账号 A 计算结果: 走节点1" --> Node1
    Dispatcher -- "账号 B 计算结果: 走节点2" --> Node2

    Node1 -- "IP: 8.8.8.1 (长期不变)" --> Target
    Node2 -- "IP: 8.8.8.2 (长期不变)" --> Target
```

原理拆解（看图说话）
这套架构的精髓在于**“网关路由分离”**，它切断了“切换 IP”和“全局网络”的关联：
唯一入口： 你们的业务代码不需要去管理错综复杂的本地端口，所有的网络请求统一打向 127.0.0.1:10086 这一个代理入口。
身份夹带： 唯一的区别是，业务代码在调用代理时，把自己的 Account_ID 当作代理的用户名塞进去（即 Proxy-Authorization 头）。
哈希路由（核心魔法）：
当“中心网关”收到请求时，它不会盲目转发。它会先拆开信封，看到“发件人是 账号 A”。
然后它进行哈希计算（例如：Hash("acc_A") % 3 = 1）。
于是，网关在内部悄悄地把这条 TCP 连接，透明无缝地桥接到本地的 10001 端口（WARP 实例 1）。
空间隔离： 因为 账号 A 的哈希结果永远是 1，所以只要账号 A 发请求，必定走 10001 端口，最终以 8.8.8.1 的固定 IP 访问 OpenAI。同理，账号 B 永远走另一个独立的出口。
最终达成的效果：
无论有多少个账号在并发运行，底层都不会去“重启或切换”任何 WARP 节点。流量在中心网关处像高速公路收费站一样被自动分流到了不同的车道，实现了完美的“多账号并发、固定 IP、互不污染”。
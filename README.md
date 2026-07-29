# kpf

> Kubernetes 端口转发的终端工具 — 守护进程常驻、TUI 交互、CLI 可脚本化。

`kubectl port-forward` 在终端关闭后隧道就断了。`kpf` 把所有端口转发交给一个后台守护进程，关闭 TUI 不会影响已建立的连接；进程退出、Pod 重启、API 抖动都能自动恢复，并能把状态持久化到 `state.json`，下次启动按原 ID 复活。

---

## 目录

- [核心特性](#核心特性)
- [架构](#架构)
- [技术栈](#技术栈)
- [快速开始](#快速开始)
- [CLI 参考](#cli-参考)
- [TUI 快捷键](#tui-快捷键)
- [状态、socket、日志](#状态socket日志)
- [环境变量](#环境变量)
- [项目结构](#项目结构)
- [故障排查](#故障排查)
- [协议说明](#协议说明)
- [许可证](#许可证)

---

## 核心特性

### 资源覆盖

- **五种资源类型**：Pod、Service、Deployment、StatefulSet、ReplicaSet。
- **自动解析 backing pod**：高阶资源（Service / Deployment / STS / RS）无需手动指定 Pod，按 ready + creationTimestamp 选最优。
- **多端口转发**：列出对象的所有 remote 端口（容器端口或 Service port），用户可以把每个 remote 映射到任意 local 端口；想要排除某个端口可以改成不会被路由到的本地端口（实际场景：forward 全部 remote 端口，客户端连不上也无所谓）。
- **绑定地址**：`0.0.0.0` 默认（局域网可达），`127.0.0.1` 收紧到本机。

### 稳定性

- **自动重连**：完整抖动指数退避（base 500ms，cap 30s），连接断开后无限次重试。
- **Stale 终结**：连续 3 次 `pod not found` 自动把 forward 标记为 `stale` 并停止重试，避免无效打日志。
- **端口冲突检测**：转发启动前 `tryListen` 探测本地端口，命中直接返回 `port_in_use`，错误信息保留原 cause。daemon 还扫描 manager 里所有已注册 forward 的 `spec.Ports`，对**已经声明但还没 bind 到内核**的端口（处于 SPDY dial 阶段）也一并拒绝——避免两次提交都被 OS 级 `tryListen` 漏判的竞态。TUI 在第 ⑤ 步还会做一次**双源预检**：本地 `bind() + close()` 探测 + 通过 `forward.claimedPorts` IPC 拉 daemon 的权威声明表，命中任何一个就在输入框右侧出现 `✗ in use` 红色提示；按 Enter 时若仍有冲突则直接拦下并提示哪个端口冲突（含持有该端口的 forward id），省掉 IPC 往返。Forward goroutine 的 `runOnce` 也补了一个 `pfErrCh` 分支——bind 失败不会再卡在 `starting` 状态。
- **监听器泄漏防护**：`shutdown()` 显式调用 `pf.Close()`，并在重试循环中关闭旧实例，杜绝僵尸 socket。
- **三层保活**：Forward 进入 Ready 后启动 `runHealthCheck` goroutine，每 `probeInterval = 5s` 对每个本地端口做一次 `net.DialTimeout(2s)`；kernel 拒绝 → `pf.Close()` → `ForwardPorts()` 经 `CloseChan` 退出 → Run 走 backoff 重连。这是应用层兜底，**不依赖** SPDY ping / TCP keepalive 是否被沿途 NAT 吃掉。三层叠加：client-go 内置 SPDY ping (5s) + kpf 主动探针 (5s) + 用户请求触发。详见 `internal/forward/forwarder.go` 的 `runHealthCheck` / `probeLocalPorts`。
- **状态原子持久化**：`state.json` 通过 temp + rename + fsync 写入，永不出现半截文件。

### 多集群与多 kubeconfig

- 自动扫描 `~/.kube`（或 `KPF_KUBECONFIG_DIR`）下的所有 kubeconfig，**按路径去重**（同一文件被多个目录链入只取一份）。
- TUI 一次列出所有可用 kubeconfig（**5 列表格**：BASENAME/CTX/CLUSTERS/CONTEXTS/USERS）；启动 forward 时按所选文件路径落库到 `state.json`，重启后 daemon 能直接按路径找回同一份 client config。

### 守护进程审计

- **启动 + 每 60s** 周期性校验：把 `forward.Manager.LivePorts()`（通过 `portforward.PortForwarder.GetPorts()` 拿到）与 `state.json` 中声明的端口做对称差。
- **孤儿 listener**（端口在监听但 state 里没有对应记录）：很可能是上一次关闭时未释放的 socket —— WARN 日志带原因与提示。
- **missing listener**（state 里有记录但内核没有对应监听）：通常是 restore 时 dial 失败 —— WARN 日志明确告知用户哪些 forward 没起来。
- **零外部依赖**：早期版本用 `lsof` 拿进程监听列表；现在直接读 `pf.GetPorts()`，不再依赖任何 shell 工具，在最小 Linux 容器、Alpine、macOS、Windows 都跑得起来。
- **`kpf doctor` CLI 入口**：把同样的对齐检查 + ping + state 健康度 + stale 计数暴露给 shell 脚本 / CI（退出码 0/1/2）。

### 交互体验

- **5 步向导 TUI**：kubeconfig → namespace → 资源类型 → 对象 → 端口。**kubeconfig** 用 `bubbles/table` 列展示（信息密度高）；**namespace / resource / object** 仍用 list 列表，保留 `/` 过滤；**端口**用 textinput。`↑/↓` 选择，`enter` 确认，`esc` 回退。
- **Active 视图**：所有正在运行的 forward **8 列表格**展示（ID / STATUS / KIND/OBJECT / NS / BIND / PORTS / CLUSTER / AGE），自动 1.5s tick + 事件订阅双驱动刷新，状态色块化（● ready / ◐ starting / ⚠ dropped / ○ stopped / ✗ stale）。光标定位选中行，`d` / `x` / `delete` 通过 Cursor 索引回 forward id 后停止。
- **Active 视图快捷键**：`d` / `x` / `delete` 一键停止选中 forward；删除结果实时回显到状态行。
- **Loading 状态**：第 ⑤ 步按 Enter 后渲染转圈 spinner，并禁用再次按 Enter，防止 k8s dial 慢时连按导致重复 forward（多个 starting 卡死）。
- **Stale "not found" 修复**：删除成功的 forward 不会再因为 IPC 二次触发而显示红色错误 —— 把 "用户按 d" 和 "IPC 返回" 拆成两个独立消息类型。

### 进程模型

- **二进制自复用**：`kpf` 进程检测到 `__daemon__` argv 时切换为守护进程模式。TUI 启动时若没探测到 socket，自己 `os.Executable()` 重启子进程，并通过 `setsid` 完全脱离父进程组（关闭终端不影响它）。
- **跨进程 IPC**：JSON over Unix Domain Socket，事件推送（`forward.ready` / `forward.dropped` / `forward.stopped` / `forward.log`）通过独立的 `forward.events` 订阅通道走。
- **可脚本化**：所有功能在 CLI 里都能调用，方便 CI / 自动化场景。

### 纯 Go

只依赖 Go 标准库 + Bubble Tea + client-go + SPDY runtime，没有 `kubectl`、没有 shell 副作用，单一二进制就能跑。

---

## 架构

### 进程拓扑

```
┌─────────────────┐                ┌──────────────────────────────┐
│   TUI / CLI     │  ── IPC ──▶   │     kpf __daemon__           │
│  (Bubble Tea)   │  ◀── events ──│  ┌────────────────────────┐  │
│                 │                │  │ forward.Manager        │  │
│  ./bin/kpf      │  Unix socket   │  │  ┌──────────────────┐  │  │
│  ./bin/kpf ls   │  ~/.local/...  │  │  │ Forward (×N)     │  │  │
│  ./bin/kpf stop │  /kpf.sock     │  │  │  + SPDY dialer   │  │  │
└─────────────────┘                │  │  │  + retry loop    │  │  │
                                   │  │  └──────────────────┘  │  │
                                   │  └────────────────────────┘  │
                                   │  + state.Store (atomic JSON) │
                                   │  + Parity Audit (60s tick)   │
                                   └──────────────────────────────┘
```

- TUI / CLI 都是**无状态客户端**，所有转发生命周期由 daemon 持有。
- Daemon 通过 `setsid` 脱离父进程组；父终端关闭不会发送 SIGHUP 给它。
- `forward.Manager` 维护 `map[id]*Forward`；每个 `Forward` 跑一个独立 goroutine 做 SPDY dial + 端口监听 + 事件广播。
- 事件通过 channel 扇出：manager 一份推给本进程的 `startPersistence`（写 state.json）+ 订阅者列表（推送给所有 IPC 客户端）。

### 包结构

```
cmd/kpf/main.go               CLI / TUI 入口 + __daemon__ 复用 fork
internal/
├── config/                   XDG-style 路径解析
├── state/                    state.json / daemon.json 原子读写
├── ipc/                      JSON-over-Unix-socket 协议（含事件订阅）
├── kubeconfig/               目录扫描 + clientcmd 加载 + 按路径去重
├── k8s/                      clientset facade + 端口提取 + selector → pod
├── forward/                  Forward + Manager + 退避 + 端口冲突检测
├── daemon/                   IPC handlers + 生命周期 + 持久化 + 审计
└── tui/                      Bubble Tea 向导（5 步 + 8 列 active 视图）
                              + IPC bridge + 事件订阅 watcher + bubbles/table
```

### IPC 协议（一行摘要）

JSON over Unix socket，每条消息 `Request{ID, Method, Params}` / `Response{ID, OK, Result, Error}`：

| Method                          | 说明                              |
| ------------------------------- | --------------------------------- |
| `ping`                          | 心跳，回报 daemon 版本与 uptime    |
| `kubeconfigs.list`              | 扫描 kubeconfig 目录                |
| `namespaces.list`               | 列命名空间                          |
| `resources.list`                | 列 Pod/Service/Deployment/...      |
| `ports.list`                    | 取对象可转发的端口                   |
| `forward.start`                 | 启动一条转发                        |
| `forward.list`                  | 列当前所有转发                      |
| `forward.stop` / `forward.stopAll` | 停止                                |
| `forward.events`                | **订阅**事件流（ready/dropped/...）|
| `forward.logs`                  | 订阅单条 forward 的 log 事件流      |
| `forward.restart`               | 用同一 spec 重启（保留 ID）         |
| `forward.claimedPorts`          | 取 daemon manager 已声明的本地端口（TUI 预检用）|
| `forward.livePorts`             | 取 kpf 实际持有的内核监听端口（doctor 用）|
| `shutdown`                      | 关闭 daemon（带外调试用）           |

完整 schema 见 [`internal/ipc/protocol.go`](internal/ipc/protocol.go)。

---

## 技术栈

| 用途              | 依赖                                                          |
| ----------------- | ------------------------------------------------------------- |
| TUI 框架          | [charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea)        |
| TUI 组件库        | [charmbracelet/bubbles](https://github.com/charmbracelet/bubbles)            |
| 终端样式          | [charmbracelet/lipgloss](https://github.com/charmbracelet/lipgloss)          |
| Kubernetes 客户端 | [k8s.io/client-go](https://github.com/kubernetes/client-go)                  |
| Kubernetes API    | [k8s.io/api](https://github.com/kubernetes/api)                              |
| SPDY 流           | [moby/spdystream](https://github.com/moby/spdystream) （client-go 间接依赖） |
| 语言              | Go 1.26+                                                     |

依赖模块完整列表见 [`go.mod`](go.mod)。

---

## 快速开始

### 编译

```sh
make build              # → bin/kpf
# 或
go build -o bin/kpf ./cmd/kpf
```

需要 Go 1.24+（开发基于 1.26），以及一个能访问的 Go module proxy。

### 安装

```sh
make build && cp bin/kpf /usr/local/bin/
# 或
go install ./cmd/kpf@latest
```

### 启动 TUI（最常用）

```sh
./bin/kpf               # 自动拉起 daemon，进入向导
```

### 仅 CLI 流程

```sh
./bin/kpf daemon start                        # 后台 daemon
./bin/kpf ping                                # 验证 daemon 在线
./bin/kpf doctor                              # 健康检查（端口对齐 / state 健康）
./bin/kpf namespaces ~/.kube/config          # 调试：列出命名空间
./bin/kpf forward start \
    --kubeconfig ~/.kube/config \
    --ns default \
    --kind Deployment \
    --object my-app \
    --ports 8080:8080,9090:9090
./bin/kpf ls --json | jq '.[].id'             # 表格化列出活跃 forward（JSON 给脚本）
./bin/kpf ls --watch                          # watch 模式：状态变化时清屏重打
./bin/kpf logs fwd_0001                       # 看日志（默认采样 500ms）
./bin/kpf logs -f fwd_0001                    # follow 模式
./bin/kpf restart fwd_0001                    # 用同一 spec 重启（保留 ID）
./bin/kpf stop fwd_0001                       # 停止单条
./bin/kpf daemon stop                         # 全部停掉
```

---

## CLI 参考

```
kpf                       # 启动 TUI（自动拉 daemon）
kpf tui                   # 同上

kpf daemon start          # 启动后台 daemon
kpf daemon stop           # 停掉 daemon（所有 forward 一起死）
kpf daemon status         # daemon 信息（PID、socket、版本）

kpf forward start ...     # 启动一条 forward
kpf forward list          # 同 `kpf ls`
kpf forward stop ID       # 同 `kpf stop ID`
kpf forward stopAll       # 同 `kpf stop --all`
kpf forward restart ID    # 同 `kpf restart ID`（用同一 spec 重启）
kpf forward events [-f] ID [ID...]    # 流式打印 forward 事件（-f 跟随直到 stdin 关闭）

kpf ls / kpf list          # 表格化列出活跃 forward（支持 --json / --watch / 过滤）
kpf stop ID                 # 停一条
kpf stop --all              # 停全部
kpf restart ID              # 用同一 spec 重启一条（保留 ID）
kpf logs [-f] <id>          # 流式打印 forward 日志
kpf ns / kpf namespaces PATH  # 列命名空间（两个别名等价）

kpf ping                  # 探活 daemon
kpf doctor                # 健康检查（daemon / socket / state / listener parity）
kpf namespaces PATH       # 列 kubeconfig 的 namespaces
kpf version               # 版本
kpf help                  # 完整帮助
```

### `kpf forward start` flags

| 标志            | 必填 | 默认       | 说明                                       |
| --------------- | ---- | ---------- | ------------------------------------------ |
| `--kubeconfig`  | ✅   |            | kubeconfig 路径                            |
| `--context`     |      | current    | kubeconfig context 名                       |
| `--ns`          | ✅   |            | Namespace                                  |
| `--kind`        |      | `Pod`      | `Pod` / `Service` / `Deployment` / `StatefulSet` / `ReplicaSet` |
| `--object`      | ✅   |            | 资源名称                                   |
| `--bind`        |      | `0.0.0.0`  | 本地绑定地址                               |
| `--ports`       | ✅   |            | `local:remote[,local:remote…]`             |
| `--pod`         |      | 自动解析   | 覆盖 pod 名（极少用到）                    |

### `kpf ls` flags

| 标志        | 说明                                                |
| ----------- | --------------------------------------------------- |
| `--json`    | 输出 JSON 数组（适合 `jq` / CI 消费）               |
| `--watch`   | watch 模式：每次 forward 状态变化时清屏重打          |
| `-w`        | 同 `--watch`                                        |
| `--ns`      | 按 namespace 过滤（精确匹配）                       |
| `--kind`    | 按资源类型过滤（`Pod`/`Service`/…，大小写不敏感）   |
| `--status`  | 按状态过滤（`ready`/`dropped`/`stale`/…，大小写不敏感）|

watch 模式下默认清屏 + 光标归位；JSON 模式下不刷屏，每行一个快照 JSON。Log 事件不会触发 watch 刷新（频次太高），只刷新 ready/dropped/stopped/stale/error。

### `kpf logs` flags

| 标志      | 说明                                              |
| --------- | ------------------------------------------------- |
| `-f`      | follow 模式：阻塞到 stdin 关闭                    |
| `--follow`| 同 `-f`                                           |

默认行为是采样 500ms 后退出，输出 `<ts>\t<stream>\t<line>`。

### `kpf doctor`

`kpf doctor` 跑一组健康检查并以退出码反映最差结果：

| 退出码 | 含义                                       |
| ------ | ------------------------------------------ |
| 0      | 全部 PASS                                  |
| 1      | 至少一个 WARN，没有 FAIL                   |
| 2      | 至少一个 FAIL                              |

输出格式：`[LEVEL] check-name: detail`，按以下顺序：

1. **daemon-reachable** — socket 能否 dial 到
2. **daemon-ping** — ping 返回 version + uptime
3. **state-list** — `forward.list` 能成功返回
4. **listener-parity** — `pf.GetPorts()` 实际监听端口 vs spec 声明端口的对称差（orphan = 监听但无 spec；missing = spec 但未监听）
5. **stale-forwards** — `status=stale` 的 forward 数量
6. **status-vocabulary** — 所有 forward 的 status 是否在已知集合内

`listener-parity` 这条把 daemon 内部的 `computeParity` 审计暴露给 CLI — 配合 TUI 的 `===[ 守护进程审计 ]` 一节，是排查"端口被别的进程占了"或"forward 注册成功但没起来"的第一站。

---

## TUI 快捷键

| 按键             | 作用                                       |
| ---------------- | ------------------------------------------ |
| `↑` / `↓`        | 上下移动                                   |
| `enter`          | 确认当前步骤                               |
| `esc`            | 回退一步                                   |
| `/`              | 过滤列表                                   |
| `a`              | 跳到 Active 视图                           |
| `d` / `x` / `delete` | 在 Active 视图：停止选中 forward        |
| `q`              | 退出 TUI（daemon 与 forward 继续运行）     |
| `ctrl+c`         | 立即退出                                   |

第 ⑤ 步按 `enter` 后会显示 `⟳ starting forward…` 转圈，**此时再按 Enter 被吞掉**，防止 k8s dial 慢时连按生成重复 forward。

Active 视图状态色块：

| 状态           | 图标       | 含义                                       |
| -------------- | ---------- | ------------------------------------------ |
| `ready`        | ● 绿色     | 已建立，正在传输                            |
| `starting`     | ◐ 黄色     | SPDY/port 协商中                            |
| `dropped`      | ⚠ 黄色     | 连接掉了，正在按退避重试                    |
| `stopped`      | ○ 红色     | 用户主动停止                                |
| `stale`        | ✗ 红色     | pod 连续 3 次 not found，停止重试           |
| `error`        | ! 红色     | 不可恢复错误                                |

### Active 视图列

8 列固定宽度，宽度和 = 115 cell，配合 cell padding=0 在 ≥120 字符终端整齐对齐：

| 列            | 宽度 | 内容                                       |
| ------------- | ---- | ------------------------------------------ |
| ID            | 9    | `fwd_NNNN` 唯一标识                        |
| STATUS        | 12   | 图标 + 状态名（颜色见上表）                |
| KIND/OBJECT   | 22   | `Pod/my-pod` 格式                          |
| NS            | 11   | Namespace（按字符截断）                    |
| BIND          | 11   | 本地绑定地址                                |
| PORTS         | 24   | `local:remote` 列表，逗号分隔              |
| CLUSTER       | 16   | kubeconfig basename                        |
| AGE           | 10   | 启动时间，按 `…` 截断                      |

超长内容走 ANSI-safe `runewidth.Truncate` / `lipgloss.NewStyle().MaxWidth(...)` 截断，保证不撕裂。`d` / `x` / `delete` 通过 `table.Cursor()` 索引回 `forwards []ipcForward` 平行切片拿到 ID，然后走 `StopForwardMsg` → `forward.stop` IPC。

### Kubeconfig 选择列

5 列固定宽度，宽度和 = 78 cell，能塞进 80 字符终端：

| 列         | 宽度 | 内容                                |
| ---------- | ---- | ----------------------------------- |
| BASENAME   | 22   | kubeconfig 文件名（按字符截断）     |
| CTX        | 22   | 当前 context                        |
| CLUSTERS   | 12   | `N clusters` 数量摘要               |
| CONTEXTS   | 12   | `N contexts` 数量摘要               |
| USERS      | 10   | `N users` 数量摘要                  |

`enter` 通过 Cursor 索引回 `entries []kubeconfig.Entry` 平行切片，发 `KubeChosenMsg` 带 Path + CurrentContext。

> 这两步用 `bubbles/table` 而不是 list，是因为**信息密度**更高（5–8 列同时可见，无需展开）。代价是失去 list 自带的 `/` 过滤 —— 当前用不上：kubeconfig 数量天然少，Active 视图用 `kpf ls --ns / --kind / --status` 在 CLI 里过滤更顺手。

---

## 状态、socket、日志

默认全部在 `~/.local/share/kpf/`：

```
~/.local/share/kpf/
├── kpf.sock          # IPC socket（Unix domain）
├── daemon.json       # { PID, Socket, StartedAt, Version, LogFile }
├── state.json        # 持久化 forwards（schema_version=1）
└── daemon.log        # slog JSON 日志（默认 Level=Warn）
```

`state.json` 字段：

```json
{
  "schema_version": 1,
  "forwards": [
    {
      "id": "fwd_0001",
      "kubeconfig": "/Users/me/.kube/dev.config",
      "namespace": "default",
      "kind": "Service",
      "object": "my-app",
      "pod_name": "my-app-7d8b9c4f-x7z2p",
      "bind": "0.0.0.0",
      "ports": [{ "local": 8080, "remote": 8080 }],
      "started_at": "2026-07-26T18:34:00+08:00",
      "status": "ready"
    }
  ]
}
```

---

## 环境变量

| 变量                  | 作用                                                |
| --------------------- | --------------------------------------------------- |
| `KPF_HOME`            | 覆盖 state 目录                                     |
| `KPF_SOCKET`          | 覆盖 daemon socket 路径                              |
| `KPF_KUBECONFIG_DIR`  | 覆盖扫描的 kubeconfig 目录                           |
| `KPF_DEBUG`           | 非空时启用 daemon debug 级日志（JSON 写到 log 文件） |

---

## 项目结构

```
kpf/
├── cmd/kpf/main.go               CLI / TUI 入口 + __daemon__ 复用 fork
├── Makefile                     build / test / daemon-start / daemon-stop 等
├── go.mod / go.sum              模块声明与依赖锁
├── bin/                         编译产物（gitignore）
└── internal/
    ├── config/                  XDG 路径解析
    ├── state/                   state.json / daemon.json 原子读写
    ├── ipc/                     JSON-over-Unix-socket 协议 + codec + server + client
    ├── kubeconfig/              目录扫描 + clientcmd 加载 + 按路径去重
    ├── k8s/                     clientset + 端口提取 + workload → pod 解析
    ├── forward/                 Forward + Manager + 退避 + 端口冲突检测 + LivePorts()
    ├── daemon/                  IPC handler + 生命周期 + 持久化 + 监听器审计
    └── tui/                     Bubble Tea 向导（5 步 + 8 列 active/5 列 kubeconfig 表）
                                  + bubbles/table + IPC bridge + 事件订阅
├── scripts/build-release.sh      跨平台 release 打包脚本（CI 也调用这个）
└── .github/workflows/            ci.yml / release.yml / stale.yml
```

---

## 开发与发布

### 日常开发

```sh
make help                     # 列出所有 target
make build                    # 构建 bin/kpf（-trimpath）
make test                     # go test -race -count=1 ./...
make vet                      # go vet ./...
make fmt                      # gofmt ./...
```

### 本地复现 CI release 构建

CI（`.github/workflows/release.yml`）和本地共用同一个脚本 — 本地打包出来的产物结构、命名、checksums 与 CI 完全一致：

```sh
make release                  # 等价 ./scripts/build-release.sh
make release VERSION=0.1.2    # 指定版本号（覆盖自动解析的 const version）

# 产物路径
ls dist/
#   kpf-<VERSION>-linux-amd64.tar.gz
#   kpf-<VERSION>-linux-arm64.tar.gz
#   kpf-<VERSION>-darwin-amd64.tar.gz
#   kpf-<VERSION>-darwin-arm64.tar.gz
#   kpf-<VERSION>-SHASUMS256.txt
```

### 发版流程

```sh
# 1. bump version
$EDITOR cmd/kpf/main.go       # 把 const version = "0.1.x" 改成新版本

# 2. 提交 + push
git commit -am "kpf: bump version to 0.1.x"
git push origin main

# 3. 打 tag（push 后 release workflow 自动触发）
make tag VERSION=0.1.x        # 等价 git tag -a v0.1.x -m "kpf v0.1.x"
git push origin v0.1.x

# → .github/workflows/release.yml 自动跑 matrix build + 聚合 + 发布
# → 资产覆盖式上传（replace_assets=true），重推同一 tag 不会留垃圾
```

---

## 故障排查

**`daemon did not become ready in 5s`**
5 秒内 socket 没出现。检查 `~/.local/share/kpf/daemon.log`，建议 `KPF_DEBUG=1` 重启拿 debug 级日志。

**`port_in_use: ...`**
本机端口已被占用（其他进程或上一次未清理的 forward）。`kpf ls` 看活跃转发；`kpf stop <id>` 或换一个本地端口。

**`forward status: stale`**
连续 3 次 `pod not found`。Pod 已被删除或重新调度。重新在 TUI 里建一条；stale 状态不会自愈。

**TUI 按 `a` 后空白**
daemon 大概率挂了。`kpf daemon status` 确认；`kpf daemon start` 重启。`state.json` 里的活跃项会被自动恢复（带稳定 ID）。

**Forwards 在 `kpf daemon stop` 后消失**
这是预期：daemon 持有连接。用 `kpf stop <id>` 停单条；用 `kpf daemon start` 重新拉起会从 `state.json` 复活所有非 `stopped` / `stale` 的 forward。

**Audit 警告 `orphan LISTEN ports`**
daemon 启动 5s 后会做一次 listener 审计。如果日志报 `orphan LISTEN ports [9999, 27017]`，说明 kernel 上有 kpf 持有的监听端口但 `state.json` 里没有对应记录 —— 通常是上一次 Stop 时 socket 未释放。daemon 重启会自动清理（每次启动都重读 state.json）；若想立即清理，`pkill -9 __daemon__` 后重启即可。

**快速排查一切**
跑 `kpf doctor`。它会用退出码（0 / 1 / 2）告诉你 daemon / socket / state / 端口对齐哪里坏了，详情看 `### kpf doctor`。配合 TUI 的 audit 章节，可以不用翻 daemon log 就定位大部分问题。

**`kpf logs <id>` 没有任何输出**
log 事件要等 forward 真有 `stdout/stderr` 流量才会产生。新建的 forward 静默期是正常的。`-f` 进 follow 模式后保持 stdout 开，等待一段时间。

**`kpf restart <id>` 返回 `not_found: ... not found in history`**
daemon 只在内存里保留 spec，**重启 daemon 后会丢**。`state.json` 也没有存（设计如此，避免历史污染）。解决办法：用 `kpf forward start ...` 重起一条，记得保留原 ID 的稳定语义就靠 `--pod` + `--bind` 一致。

---

## 协议说明

TUI 与 daemon 之间的 IPC 协议完整 schema 在 [`internal/ipc/protocol.go`](internal/ipc/protocol.go)，要点：

- 单连接多方法复用：每个连接独立读 goroutine 循环，按帧长度解码 `Request{ID, Method, Params}`。
- 错误码见 `ErrCode*` 常量；`not_found` / `port_in_use` / `auth_error` / `kube_error` / `bad_request` / `internal` / `already_exists` / `unknown_method`。
- 事件订阅走单独帧 `Event{Event, ForwardID, Payload}`，无 ID，由 `forward.events` 方法开启长连接。

---

## 许可证

待定。
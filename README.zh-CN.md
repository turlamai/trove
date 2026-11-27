# Trove

面向 Unraid WebGUI 的存储感知文件管理器——以共享（Share）为先的浏览、可选的双窗格工作流、服务端后台任务（暂存 → 校验 → 原子提交）。它从设计上拒绝 Unraid 上最容易丢数据的操作：把逻辑共享和物理磁盘视图混在一次复制/移动里。

[![CI](https://github.com/turlamai/trove/actions/workflows/ci.yml/badge.svg)](https://github.com/turlamai/trove/actions/workflows/ci.yml) [![License](https://img.shields.io/badge/license-MIT-blue)](LICENSE) [![Version](https://img.shields.io/github/v/release/turlamai/trove?color=C15F3C&label=version)](https://github.com/turlamai/trove/releases/latest)

[English](README.md) · **中文**

> **开发预览版。** 尚未在真实机器上完成完整安全矩阵验收
> （[docs/release-checklist.md](docs/release-checklist.md)）。请先在测试机上
> 安装试用；若要在生产机使用，请先阅读
> [docs/safety.md](docs/safety.md) 自行评估。
> 在配置任务数据目录之前，Trove 处于**只读模式**。

## 从这里开始

Trove 是原生插件：走 Unraid 插件机制安装，入口在 **工具 → Trove**，跟随
WebGUI 明暗主题，不改动系统内置文件管理器。

核心能力：

- **共享优先**——导航以逻辑共享为主；物理磁盘与缓存池在专家模式下按需显示，默认只读
- **单/双窗格**——双窗格独立路径与历史，支持拖拽或 F6 传输，目标路径始终可见
- **后台任务**——复制/移动/删除/哈希由 root 守护进程执行并持久化日志，关页面无影响
- **存储安全检查**——共享↔磁盘命名空间混用会被硬性拦截并解释原因（不是可以点掉的警告）；同文件别名、递归目标、源文件变化全部在服务端把关
- **冲突处理**——逐项选择跳过/覆盖/保留两者，并对照源与目标信息；覆盖永不作为默认项
- **详情侧栏**——只展示可验证的元数据；无法验证的一律显示"未知"
- **中文/英文界面**，键盘可完整操作，跟随 Unraid 明暗主题

V1 有意不做：上传、媒体预览、缩略图、回收站，以及对 `appdata`/`domains`/`system`/`isos` 的未确认写入。详见
[docs/compatibility-matrix.md](docs/compatibility-matrix.md)。

## 安装（开发预览版）

1. 从[最新 Release](https://github.com/turlamai/trove/releases/latest) 取得
   `trove.plg` 地址：`https://raw.githubusercontent.com/turlamai/trove/main/trove.plg`
2. Unraid WebGUI → **插件 → 安装插件**，粘贴地址。
3. **设置 → Trove**：配置*任务数据目录*（阵列或池上的目录，如
   `/mnt/user/trove-data`，不要放 USB 闪存）。配置之前插件只读。
4. 打开 **工具 → Trove**。

安装前请校验发布物：
`shasum -a 256 --check --ignore-missing SHA256SUMS`。插件清单内同样固化了安装包的 SHA-256。

## 安全是怎么做的

每次写操作都先在服务端做"预检"：守护进程扫描源树、识别冲突与风险，签发短时效的确认令牌；执行阶段每个文件先写入目标目录的暂存文件，fsync、校验后原子提交；移动操作只在"复制完整、校验通过、源未变化"之后才删除源——中途变化的源一律保留。路径越界由内核 `openat2(RESOLVE_BENEATH)` 约束，内核不支持时写入直接禁用。

细节：[docs/architecture.md](docs/architecture.md) ·
[docs/threat-model.md](docs/threat-model.md) ·
[docs/safety.md](docs/safety.md)

## 仓库结构

```text
trove/
├── daemon/          Go 守护进程（roots / resolver / planner / executor / jobs / api）
├── frontend/        Preact + Vite 单页应用（无 CDN 依赖）
├── plugin/          .plg 清单 + WebGUI 文件（.page、PHP 网关、事件钩子）
├── scripts/         构建与打包（txz、SHA-256、图标）
├── docs/            架构、威胁模型、API 契约、兼容矩阵、发布清单、ADR、产品规格书
└── .github/         CI（测试 + 打包演练）与发布流水线
```

## 开发

```sh
# 守护进程
cd daemon && go test ./... && go vet ./...

# 前端（dev server 代理到本地守护进程）
cd frontend && npm install && npm run dev

# 完整打包（需要版本号）
./scripts/build.sh 0.1.0
```

本地跑守护进程请用合成根目录（不要指向真实数据）：

```sh
mkdir -p devroot/share1 devroot/share2
cd daemon && go run ./cmd/troved --dev-roots ../devroot --dev-tcp 127.0.0.1:8787 \
    --socket /tmp/troved.sock --token-file /tmp/troved.token \
    --config /tmp/trove.cfg --data-dir /tmp/trove-data
```

Linux syscall 层（openat2 约束、executor 语义）的测试在 Linux CI 上运行。

## 状态与路线

- **V1 + V1.5 核心（本预览）**——浏览、双窗格、安全操作、任务中心、设置、多语言、**上传、图片/文本/PDF 预览、共享级回收站（恢复/彻底删除）、Dashboard 磁贴**
- **下一步**——批量改名预演、压缩包、基于 Range 的媒体预览
- 物理视图写入保持默认关闭；跨视图混用无条件拒绝

## 许可

[MIT](LICENSE)

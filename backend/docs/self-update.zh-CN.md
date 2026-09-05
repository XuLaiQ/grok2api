# 网页版本更新

Language: [English](self-update.md) | 简体中文

管理员可以在 **设置 -> 关于** 中保存 GitHub 仓库地址，检查最新正式 Release，然后确认安装。下载、SHA-256 校验、解压、服务重启和启动失败回退由服务端完成。安装期间可以离开页面，重新打开后继续查看状态。

## 首次启用

现有服务器需先部署一次包含此功能的版本。当前 Compose 使用本地构建：

```bash
docker compose up -d --build grok2api
```

镜像默认以 `--supervise` 启动，更新目录 `/app/data/updates` 位于现有 `grok2api-data` 数据卷内，无需 Docker socket、宿主机脚本或容器内编译工具。不要删除此数据卷。

仓库初始为空，不会自动检查或安装原项目。部署后进入 **设置 -> 关于**，填写 `owner/repository` 或 `https://github.com/owner/repository`，保存并检查更新。仅支持公开 GitHub 仓库的正式 Release；服务器必须能够访问 GitHub API 和 Release 下载域名。

## 发布新版本

仓库内的 `.github/workflows/release.yml` 会在推送 `v*` 标签时构建完整发布包，不依赖仓库所属账号。

1. 将代码推送到自己的 GitHub 仓库，并启用 Actions。
2. 修改根目录 `VERSION`，例如 `v3.1.6`，提交该修改。
3. 创建与 `VERSION` 完全一致的标签并推送。
4. 等待 **Release bundles** 工作流完成；随后可在管理端检查并安装。

```bash
git tag v3.1.6
git push origin v3.1.6
```

示例版本必须高于当前运行版本。单纯提交代码、推送分支或只有 GitHub 自动生成的源码压缩包，不会产生可安装更新。不要覆盖已经发布的标签和安装包。

正式 Release 包含：

```text
grok2api_v3.1.6_linux_amd64.tar.gz
grok2api_v3.1.6_linux_arm64.tar.gz
checksums.txt
```

每个压缩包内包含 `grok2api`、`VERSION` 和 `frontend/dist/`。工作流将标签注入后端版本号，并对压缩包生成 SHA-256 校验。安装时会重新从配置的仓库核对最新版本，拒绝版本变化、降级、错误校验和、不完整安装包以及不安全的归档路径。

## 配置

| 配置 | 默认值 | 用途 |
| --- | --- | --- |
| `updates.directory` | `./data/updates` | 更新源、状态和版本文件目录，相对路径以配置文件为基准 |
| `GROK2API_UPDATE_DIR` | 未设置 | 覆盖更新目录；Docker 默认 `/app/data/updates` |
| `GROK2API_UPDATE_REPOSITORY` | 空 | 初始仓库地址；管理端保存的配置优先 |

已选择的应用版本保存在更新目录中，普通服务重启或容器重建后继续运行该版本。该机制更新的是应用文件，容器镜像标签不会随网页安装而改变。

Linux amd64/arm64 的二进制部署也可启用托管启动：

```bash
./grok2api --supervise --config /absolute/path/config.yaml
```

需要保留稳定的启动二进制及配置路径，并让运行用户能够写入更新目录。普通 `go run` 或未启用托管启动的进程可以配置仓库和检查更新，不能执行网页安装。Windows、macOS 和多实例部署不会开放此安装方式。

## 更新过程与限制

- 安装任务由管理员启动，重复点击不会并行安装。发布包下载和校验期间旧程序继续运行；切换时会有短暂中断，长连接或长时间请求可能需要重试。
- 启动检查会验证新进程的独立标识和启动恢复完成状态，避免将旧进程或尚未准备好的服务误判为更新成功。新程序启动失败会尝试恢复前一个程序版本，并清理失败安装包。
- 自动回退仅恢复后端和前端程序文件，不回滚数据库、媒体或运行设置。涉及数据库不兼容迁移的版本应提前备份并通过部署流程升级。
- 重启时被中断的下载会标记失败，可以重新安装；页面会恢复读取服务端状态，并在确认目标版本运行成功后刷新前端。
- 发布方控制安装包内容。SHA-256 校验用于核对下载完整性，仓库应由你信任和控制。
- 操作系统依赖、启动监督程序本身、部署配置和跨实例滚动更新仍需要部署平台或镜像升级。历史发布目录会保留，需预留下载与解压空间。

## 管理接口

所有接口均位于 `/api/admin/v1`，使用现有管理员鉴权。

| 方法和路径 | 请求 | 返回 |
| --- | --- | --- |
| `GET /system/version` | 无 | 当前版本、Release 信息、仓库和安装可用性 |
| `PUT /system/update/config` | `{"repository":"owner/repository"}` | 保存后的版本信息；空字符串清除仓库 |
| `POST /system/update/check` | 无 | 最新检查结果 |
| `GET /system/update/status` | 无 | 可跨页面与重启读取的安装状态 |
| `POST /system/update/install` | `{"version":"v3.1.6"}` | `202` 和已受理的安装任务 |

# Grok2API

简体中文 | [English](README.en.md)

基于 Go 和 React 的多账号 API 网关，统一管理 Grok Build、Grok Web 与 Grok Console，提供兼容 OpenAI 和 Anthropic 的接口。

## 主要功能

- 账号导入、额度同步、凭据续期和模型路由。
- 对话、图片、视频和语音接口，具体能力以账号及模型为准。
- 客户端密钥、请求审计、代理配置和网页管理端。
- 在网页检查 GitHub 版本并安装更新，支持进度查看和启动失败回退。

## 快速部署

需要 Docker 和 Docker Compose，支持 `linux/amd64`、`linux/arm64`。下载或克隆本项目后，在项目根目录执行：

```bash
cp config.example.yaml config.yaml
openssl rand -hex 32
openssl rand -base64 32
```

将生成的 Hex 和 Base64 密钥分别填入 `config.yaml`，并设置管理员密码：

```yaml
secrets:
  jwtSecret: "替换为生成的 Hex 密钥"
  credentialEncryptionKey: "替换为生成的 Base64 密钥"

bootstrapAdmin:
  username: "admin"
  password: "替换为强密码"
```

构建并启动服务：

```bash
docker compose up -d --build grok2api
docker compose logs -f grok2api
```

访问 [http://127.0.0.1:8000](http://127.0.0.1:8000)，使用配置中的管理员账号登录。默认单实例使用 SQLite，数据库、媒体和更新文件保存在 `grok2api-data` 数据卷中。

## 使用方法

1. 登录管理端，导入或授权 Build、Web、Console 账号。
2. 等待同步完成，在模型页面确认可用模型。
3. 创建客户端密钥，并将客户端的 API 地址设为 `http://服务器地址:8000/v1`。

查询可用模型：

```bash
curl http://127.0.0.1:8000/v1/models \
  -H "Authorization: Bearer YOUR_CLIENT_KEY"
```

使用返回的模型名发起请求：

```bash
curl http://127.0.0.1:8000/v1/responses \
  -H "Authorization: Bearer YOUR_CLIENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"YOUR_MODEL","input":"你好","stream":true}'
```

其他接口与示例可在管理端的“文档”页面查看。

## 网页更新

进入 **运行设置 -> 关于与更新**，保存自己的公开 GitHub 仓库地址，检查版本后点击 **安装更新**。

现有服务器需首次部署包含此功能的版本。网页安装支持 Linux 单实例托管启动，Docker 默认已启用；更新期间服务会短暂重启，启动失败时回退程序文件，不回滚数据库。

仓库需发布包含完整安装包与校验文件的正式 Release，仅提交代码不能产生可安装更新。项目已提供 GitHub Actions 发布工作流，具体步骤见 [网页更新与发布说明](backend/docs/self-update.zh-CN.md)。

## 配置说明

- 完整配置见 [config.example.yaml](config.example.yaml)，运行参数可在管理端设置。
- 首次登录后修改管理员密码；账号写入后保留原有 `credentialEncryptionKey`，避免已有凭据无法解密。
- 妥善备份配置、数据库和媒体，不要提交密钥或账号数据，不要删除持久化数据卷。
- 公网部署使用 HTTPS，并设置 `auth.secureCookies: true`。
- 多实例部署需要 PostgreSQL、Redis 和共享媒体目录，具体要求见 [后端说明](backend/README.md)。

## 开发文档

- [后端运行与开发](backend/README.md)
- [前端运行与构建](frontend/README.md)
- [可选出口质量守护](tools/egress-quality-guard/README.zh-CN.md)

## 许可证

本项目遵循 [MIT License](LICENSE)，保留原作者的版权与许可声明。

## 项目来源与致谢

本项目基于开源项目 **Grok2API** 进行二次开发。

原项目 GitHub 地址：[https://github.com/chenyme/grok2api](https://github.com/chenyme/grok2api)

感谢原作者 **Chenyme** 及所有贡献者提供的开源代码、文档与持续维护，为本项目的开发提供了基础。

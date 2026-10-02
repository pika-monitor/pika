# Pika Web

`web/` 是官方管理后台前端（React/Vite），发布到 `/admin/assets/*`。

官方默认公开主题是独立项目 [`pika-monitor/pika-default-theme`](https://github.com/pika-monitor/pika-default-theme)。打包时默认拉取该仓库默认分支的最新代码；本地开发可以使用同级目录 `../pika-default-theme`。

## 开发

```bash
cd web && npm ci && npm run dev                  # 管理后台，http://localhost:5174/admin/
cd ../pika-default-theme && npm ci && npm run dev # 默认主题，http://localhost:5173/
```

管理后台开发服务器保留 `/admin/*`，并将其他路径代理到 `http://localhost:8080`。因此启动 Pika
后端后，可以通过 `http://localhost:5174/` 访问当前公开主题，通过
`http://localhost:5174/admin/` 访问管理后台。默认主题项目的开发服务器也会把 `/api/*`
代理到 Pika 后端，两者可以同时启动。

## 构建

`make build-web` 构建 `web/` 和独立的默认主题，将两者放到独立的发布目录：

- 管理后台：`web/dist/`；
- 默认主题：`themes/default/`。

本地开发需要使用已有主题源码（包括未提交的修改）时，可以显式指定目录，跳过远端拉取：

```bash
make DEFAULT_THEME_DIR=/path/to/pika-default-theme build-web
```

GitHub Actions 的测试镜像和正式发布每次都会检出默认主题仓库默认分支的最新提交，不再锁定主题版本。同一个 Pika 版本重新打包时，可能包含更新后的默认主题。未指定 `DEFAULT_THEME_DIR` 的本地构建也会拉取最新源码，需要网络连接；拉取或构建失败时打包失败，不回退到旧版本。

## 运行路径

- 公开主题：`/`、`/servers/*`、`/monitors/*`；
- 管理后台：`/admin/*`，包括 `/admin/login` 和 OAuth/OIDC 回调；
- 管理后台资源：`/admin/assets/*`；
- 活动主题资源：`/t/*`。

# my-navs

一个轻量的私人导航页，带后台管理和密码保护。

- 单个 Go 二进制，零第三方依赖，镜像约 10MB，运行内存十几 MB
- 数据存在一个 JSON 文件里，图标存在本地，不依赖外部 CDN
- 导航页：分组标签、跨分组搜索（`/` 聚焦，回车打开第一个结果）、深色模式、手机适配
- 后台：分组和链接的增删改、拖拽排序、从 [dashboard-icons](https://github.com/homarr-labs/dashboard-icons) 按名称获取图标、上传或粘贴图片、导入导出备份
- 所有页面都需要先输入密码；同一 IP 连续输错 5 次锁定 15 分钟

## 部署

### 1. 构建镜像

只有推送 `v` 开头的 tag 才会触发构建，普通提交不会。Actions 会构建 `linux/amd64` 和 `linux/arm64` 两个架构的镜像，推送到 `ghcr.io/<你的用户名>/my-navs`，镜像标签就是 tag 名，同时更新 `latest`：

```bash
git tag v1.0.0
git push origin v1.0.0
```

推送后会得到 `my-navs:v1.0.0` 和 `my-navs:latest` 两个标签。

镜像默认是私有的。两种办法让 VPS 拉得到：

- 在 GitHub 的 Packages 页面把 `my-navs` 设为 Public
- 或者在 VPS 上登录：在 GitHub 创建一个只有 `read:packages` 权限的 token，然后执行 `docker login ghcr.io -u <你的用户名>`，密码填这个 token

### 2. 在 VPS 上运行

```bash
mkdir -p ~/my-navs/data && cd ~/my-navs
# 容器以非 root 用户（UID 65532）运行，数据目录要给它写权限
sudo chown 65532:65532 data
```

把仓库里的 `docker-compose.yml` 和 `.env.example` 复制过来：

```bash
cp .env.example .env
```

编辑 `.env` 设置密码，再把 `docker-compose.yml` 里的 `your-github-name` 换成你的 GitHub 用户名（小写），然后启动：

```bash
docker compose up -d
```

打开 `http://服务器IP:8080` 即可。更新版本：

```bash
docker compose pull && docker compose up -d
```

## 环境变量

| 变量 | 说明 | 默认值 |
|---|---|---|
| `NAV_PASSWORD` | 访问密码，**必填** | |
| `ADMIN_PASSWORD` | 后台密码；不设则与访问密码相同 | 同 `NAV_PASSWORD` |
| `SITE_TITLE` | 页面标题 | `我的导航` |
| `SESSION_DAYS` | 登录有效天数，经常访问会自动续期 | `30` |
| `TRUST_PROXY` | 前面有反代时设为 `true`，登录限流才能拿到真实 IP | `false` |
| `HTTPS_PROXY` | 服务器访问不了 GitHub / jsDelivr 时，下载图标走的代理 | |
| `PORT` | 监听端口 | `8080` |

两个密码不同时：用访问密码登录只能看导航页，进后台需要管理密码。修改密码并重启容器后，之前的登录全部失效。

## 建议套上 HTTPS

没有 HTTPS 的话密码是明文传输的。如果 VPS 上已经有 Nginx 或 Caddy，把 compose 里的端口改成 `127.0.0.1:8080:8080`，再在 `.env` 里设置 `TRUST_PROXY=true`。

Caddy：

```
nav.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Nginx：

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    client_max_body_size 10m;
}
```

`Host` 头必须原样转发，否则后台的保存请求会被跨站检查拦下来。

## 备份

所有数据都在 `data/` 目录：

```
data/
├── navs.json   # 分组和链接
├── icons/      # 图标文件
└── .secret     # 登录会话的签名密钥
```

直接复制这个目录就是完整备份。也可以在后台点「导出」，得到一个包含图标的 JSON 文件，以后用「导入」恢复。

## 本地开发

需要 Go 1.24 以上：

```bash
NAV_PASSWORD=test go run .
```

然后打开 http://localhost:8080 。运行测试：

```bash
go test ./...
```

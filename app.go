package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const csp = "default-src 'self'; img-src 'self' data: blob:; connect-src 'self' https://cdn.jsdelivr.net; " +
	"script-src 'self'; style-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

type App struct {
	cfg      *Config
	store    *Store
	auth     *Auth
	icons    *Icons
	pages    map[string]*template.Template
	static   fs.FS
	assetVer string
}

func NewApp(cfg *Config, store *Store, auth *Auth, icons *Icons) (*App, error) {
	static, err := fs.Sub(webFS, "web/static")
	if err != nil {
		return nil, err
	}
	ver, err := hashFS(static)
	if err != nil {
		return nil, err
	}
	a := &App{cfg: cfg, store: store, auth: auth, icons: icons, static: static, assetVer: ver, pages: map[string]*template.Template{}}

	funcs := template.FuncMap{
		"asset":   func(name string) string { return "/static/" + name + "?v=" + ver },
		"initial": initial,
		"host":    displayHost,
		"searchKey": func(l Link) string {
			return strings.ToLower(l.Name + " " + l.URL + " " + l.Desc)
		},
	}
	for _, name := range []string{"login.html", "index.html", "admin.html"} {
		t, err := template.New(name).Funcs(funcs).ParseFS(webFS, "web/templates/layout.html", "web/templates/"+name)
		if err != nil {
			return nil, err
		}
		a.pages[name] = t
	}
	return a, nil
}

func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	mux.Handle("GET /static/", a.staticHandler())
	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.loginSubmit)
	mux.HandleFunc("POST /logout", a.logout)

	mux.Handle("GET /{$}", a.require(RoleUser, a.indexPage))
	mux.Handle("GET /icons/{name}", a.require(RoleUser, a.icons.Serve))

	mux.Handle("GET /admin", a.require(RoleAdmin, a.adminPage))
	mux.Handle("GET /api/data", a.require(RoleAdmin, a.apiGetData))
	mux.Handle("PUT /api/data", a.require(RoleAdmin, a.apiPutData))
	mux.Handle("POST /api/icons/upload", a.require(RoleAdmin, a.apiUploadIcon))
	mux.Handle("POST /api/icons/fetch", a.require(RoleAdmin, a.apiFetchIcon))
	mux.Handle("GET /api/export", a.require(RoleAdmin, a.apiExport))
	mux.Handle("POST /api/import", a.require(RoleAdmin, a.apiImport))
	return a.secure(mux)
}

// ---------- 中间件 ----------

func (a *App) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", csp)
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			http.Error(w, "跨站请求被拒绝", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin 拦截跨站的写请求（CSRF）。
func sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
		// 旧浏览器，或者非 HTTPS 下浏览器不发 Sec-Fetch-* 头，退回比较 Origin
	default:
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host || u.Host == r.Header.Get("X-Forwarded-Host")
}

type roleKey struct{}

func (a *App) require(need Role, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role, exp := a.auth.Session(r)
		if role < need {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				if role == RoleNone {
					writeError(w, http.StatusUnauthorized, "未登录或登录已过期")
				} else {
					writeError(w, http.StatusForbidden, "需要管理权限")
				}
				return
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		// 有效期过半时自动续期，经常用就不会掉登录
		if time.Until(exp) < a.auth.ttl/2 {
			a.auth.SetCookie(w, r, role)
		}
		h(w, r.WithContext(context.WithValue(r.Context(), roleKey{}, role)))
	})
}

func roleOf(r *http.Request) Role {
	role, _ := r.Context().Value(roleKey{}).(Role)
	return role
}

func (a *App) staticHandler() http.Handler {
	fsrv := http.StripPrefix("/static/", http.FileServerFS(a.static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("v") == a.assetVer {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fsrv.ServeHTTP(w, r)
	})
}

// ---------- 页面 ----------

type loginView struct {
	Title     string
	Error     string
	Next      string
	NeedAdmin bool
}

type groupView struct {
	Group
	Links []Link
}

type indexView struct {
	Title   string
	Groups  []groupView
	IsAdmin bool
}

func (a *App) render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := a.pages[name].ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("渲染 %s 失败：%v", name, err)
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func (a *App) indexPage(w http.ResponseWriter, r *http.Request) {
	d := a.store.Snapshot()
	byGroup := make(map[string][]Link, len(d.Groups))
	for _, l := range d.Links {
		byGroup[l.GroupID] = append(byGroup[l.GroupID], l)
	}
	view := indexView{Title: a.cfg.SiteTitle, IsAdmin: roleOf(r) == RoleAdmin}
	for _, g := range d.Groups {
		view.Groups = append(view.Groups, groupView{Group: g, Links: byGroup[g.ID]})
	}
	a.render(w, http.StatusOK, "index.html", view)
}

func (a *App) adminPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, http.StatusOK, "admin.html", struct{ Title string }{a.cfg.SiteTitle})
}

func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	need := requiredRole(next)
	role, _ := a.auth.Session(r)
	if role >= need {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	a.render(w, http.StatusOK, "login.html", loginView{
		Title:     a.cfg.SiteTitle,
		Next:      next,
		NeedAdmin: need == RoleAdmin && role == RoleUser,
	})
}

func (a *App) loginSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	r.ParseForm()
	next := safeNext(r.PostFormValue("next"))
	view := loginView{Title: a.cfg.SiteTitle, Next: next}
	ip := a.auth.ClientIP(r)
	now := time.Now()

	if d := a.auth.limiter.Blocked(ip, now); d > 0 {
		view.Error = fmt.Sprintf("尝试次数过多，请 %d 分钟后再试", int(d.Minutes())+1)
		a.render(w, http.StatusTooManyRequests, "login.html", view)
		return
	}

	role := a.auth.Check(r.PostFormValue("password"))
	if role == RoleNone {
		time.Sleep(500 * time.Millisecond)
		if a.auth.limiter.Fail(ip, now) > 0 {
			view.Error = fmt.Sprintf("尝试次数过多，请 %d 分钟后再试", int(lockDuration.Minutes()))
		} else {
			view.Error = "密码错误"
		}
		log.Printf("登录失败：%s", ip)
		a.render(w, http.StatusUnauthorized, "login.html", view)
		return
	}

	a.auth.limiter.Reset(ip)
	a.auth.SetCookie(w, r, role)
	if role < requiredRole(next) {
		// 访问密码对了但要进后台，保留登录状态，提示换管理密码
		view.Error = "这个密码没有后台权限，请输入管理密码"
		view.NeedAdmin = true
		a.render(w, http.StatusForbidden, "login.html", view)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	a.auth.ClearCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func requiredRole(path string) Role {
	if path == "/admin" || strings.HasPrefix(path, "/admin/") || strings.HasPrefix(path, "/admin?") || strings.HasPrefix(path, "/api/") {
		return RoleAdmin
	}
	return RoleUser
}

// safeNext 只允许跳转到本站路径，防止开放重定向。
func safeNext(s string) string {
	if s == "" || s[0] != '/' || strings.HasPrefix(s, "//") || strings.ContainsAny(s, "\\\r\n\t") {
		return "/"
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return s
}

// ---------- API ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v)
}

// writeStoreError 把保存失败的原因转成合适的状态码。
func writeStoreError(w http.ResponseWriter, err error) {
	var inv *invalidError
	switch {
	case errors.Is(err, ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, inv.msg)
	default:
		log.Printf("保存数据失败：%v", err)
		writeError(w, http.StatusInternalServerError, "保存失败："+err.Error())
	}
}

func (a *App) apiGetData(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.Snapshot())
}

func (a *App) apiPutData(w http.ResponseWriter, r *http.Request) {
	var d Data
	if err := decodeJSON(w, r, &d, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	nd, err := a.store.Replace(d, d.Version)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	a.icons.GC(nd.IconSet())
	writeJSON(w, http.StatusOK, nd)
}

func (a *App) apiUploadIcon(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxIconSize))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "图片不能超过 512KB")
		return
	}
	name, err := a.icons.Save(b)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"icon": name})
}

func (a *App) apiFetchIcon(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(w, r, &req, 4<<10); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	name, err := a.icons.Fetch(r.Context(), req.URL)
	if err != nil {
		// 502 表示服务器连不上远程，前端会改由浏览器下载；其他情况重试也没用
		status := http.StatusBadGateway
		var rs remoteStatusError
		if errors.As(err, &rs) || errors.Is(err, errBadImage) {
			status = http.StatusUnprocessableEntity
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"icon": name})
}

// backup 是导出文件的格式，图标以 base64 内嵌，一个文件就能完整恢复。
type backup struct {
	App   string            `json:"app"`
	Data  Data              `json:"data"`
	Icons map[string][]byte `json:"icons"`
}

func (a *App) apiExport(w http.ResponseWriter, r *http.Request) {
	d := a.store.Snapshot()
	bk := backup{App: "my-navs", Data: d, Icons: map[string][]byte{}}
	for name := range d.IconSet() {
		if b, err := a.icons.Read(name); err == nil {
			bk.Icons[name] = b
		}
	}
	filename := "my-navs-" + time.Now().Format("20060102-150405") + ".json"
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	writeJSON(w, http.StatusOK, bk)
}

func (a *App) apiImport(w http.ResponseWriter, r *http.Request) {
	var bk backup
	if err := decodeJSON(w, r, &bk, 10<<20); err != nil {
		writeError(w, http.StatusBadRequest, "文件格式不对，或者超过了 10MB")
		return
	}
	if bk.App != "my-navs" {
		writeError(w, http.StatusBadRequest, "这不是 my-navs 导出的备份文件")
		return
	}
	// 按内容重新保存图标；如果文件名对不上，以实际内容算出的名字为准
	rename := map[string]string{}
	for name, b := range bk.Icons {
		saved, err := a.icons.Save(b)
		if err != nil {
			writeError(w, http.StatusBadRequest, "图标 "+name+" 无效："+err.Error())
			return
		}
		rename[name] = saved
	}
	for i := range bk.Data.Links {
		if n, ok := rename[bk.Data.Links[i].Icon]; ok {
			bk.Data.Links[i].Icon = n
		}
	}
	nd, err := a.store.Replace(bk.Data, -1)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	a.icons.GC(nd.IconSet())
	writeJSON(w, http.StatusOK, nd)
}

// ---------- 模板辅助 ----------

func initial(name string) string {
	for _, r := range strings.TrimSpace(name) {
		return string(unicode.ToUpper(r))
	}
	return "?"
}

// displayHost 把地址简化成「主机:端口/路径」显示在卡片上，去掉协议和末尾斜杠。
func displayHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host + strings.TrimSuffix(u.EscapedPath(), "/")
}

func hashFS(fsys fs.FS) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		io.WriteString(h, path)
		h.Write(b)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:10], nil
}

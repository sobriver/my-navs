package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testAuth(nav, admin string) *Auth {
	return NewAuth(&Config{NavPassword: nav, AdminPassword: admin, SessionTTL: time.Hour}, []byte("0123456789abcdef0123456789abcdef"))
}

func TestCheckPassword(t *testing.T) {
	a := testAuth("nav", "admin")
	if r := a.Check("nav"); r != RoleUser {
		t.Fatalf("访问密码应得到 RoleUser，实际 %v", r)
	}
	if r := a.Check("admin"); r != RoleAdmin {
		t.Fatalf("管理密码应得到 RoleAdmin，实际 %v", r)
	}
	if r := a.Check("wrong"); r != RoleNone {
		t.Fatalf("错误密码应得到 RoleNone，实际 %v", r)
	}
	same := testAuth("same", "same")
	if r := same.Check("same"); r != RoleAdmin {
		t.Fatalf("两个密码相同时应视为管理员，实际 %v", r)
	}
}

func TestToken(t *testing.T) {
	a := testAuth("nav", "admin")
	now := time.Now()
	tok := a.Issue(RoleAdmin, now)

	if r, _ := a.Verify(tok, now); r != RoleAdmin {
		t.Fatalf("有效令牌校验失败：%v", r)
	}
	if r, _ := a.Verify(tok, now.Add(2*time.Hour)); r != RoleNone {
		t.Fatal("过期令牌不应通过")
	}
	// 把角色改成 1 冒充普通用户，或者把 1 改成 2 提权，签名都会对不上
	forged := "1" + tok[1:]
	if r, _ := a.Verify(forged, now); r != RoleNone {
		t.Fatal("篡改过的令牌不应通过")
	}
	userTok := a.Issue(RoleUser, now)
	if r, _ := a.Verify("2"+userTok[1:], now); r != RoleNone {
		t.Fatal("提权令牌不应通过")
	}
	// 改了管理密码后旧令牌失效
	changed := testAuth("nav", "admin2")
	if r, _ := changed.Verify(tok, now); r != RoleNone {
		t.Fatal("改密码后旧令牌不应通过")
	}
	if r, _ := changed.Verify(userTok, now); r != RoleUser {
		t.Fatal("只改管理密码时，普通用户令牌应继续有效")
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	for i := 0; i < maxFails-1; i++ {
		if l.Fail("1.2.3.4", now) > 0 {
			t.Fatalf("第 %d 次失败不应锁定", i+1)
		}
	}
	if l.Fail("1.2.3.4", now) == 0 {
		t.Fatal("达到上限应锁定")
	}
	if l.Blocked("1.2.3.4", now) == 0 {
		t.Fatal("应处于锁定状态")
	}
	if l.Blocked("5.6.7.8", now) != 0 {
		t.Fatal("其他 IP 不应受影响")
	}
	if l.Blocked("1.2.3.4", now.Add(lockDuration+time.Second)) != 0 {
		t.Fatal("锁定时间过后应解锁")
	}
}

func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"":                    "/",
		"/admin":              "/admin",
		"/?a=1":               "/?a=1",
		"//evil.com":          "/",
		"/\\evil.com":         "/",
		"https://evil.com":    "/",
		"javascript:alert(1)": "/",
	}
	for in, want := range cases {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestNormalize(t *testing.T) {
	valid := func() Data {
		return Data{
			Groups: []Group{{ID: "g1", Name: " Home "}},
			Links:  []Link{{ID: "l1", GroupID: "g1", Name: "pve", URL: "https://pve.example.com"}},
		}
	}
	d := valid()
	if err := d.normalize(); err != nil {
		t.Fatalf("合法数据校验失败：%v", err)
	}
	if d.Groups[0].Name != "Home" {
		t.Fatal("名称应去掉首尾空格")
	}

	bad := map[string]func(*Data){
		"javascript 地址": func(d *Data) { d.Links[0].URL = "javascript:alert(1)" },
		"分组不存在":         func(d *Data) { d.Links[0].GroupID = "nope" },
		"分组 ID 重复":      func(d *Data) { d.Groups = append(d.Groups, Group{ID: "g1", Name: "x"}) },
		"空名称":           func(d *Data) { d.Links[0].Name = "  " },
		"非法图标名":         func(d *Data) { d.Links[0].Icon = "../../etc/passwd" },
		"非法 ID":         func(d *Data) { d.Links[0].ID = "a b" },
	}
	for name, mutate := range bad {
		d := valid()
		mutate(&d)
		var inv *invalidError
		if err := d.normalize(); !errors.As(err, &inv) {
			t.Errorf("%s：应返回校验错误，实际 %v", name, err)
		}
	}
}

func TestStoreConflict(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "navs.json"))
	if err != nil {
		t.Fatal(err)
	}
	d := s.Snapshot()
	d.Groups = append(d.Groups, Group{ID: "g2", Name: "Cloud"})
	nd, err := s.Replace(d, d.Version)
	if err != nil {
		t.Fatal(err)
	}
	if nd.Version != d.Version+1 {
		t.Fatalf("版本号应递增，实际 %d", nd.Version)
	}
	if _, err := s.Replace(d, d.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("用旧版本号提交应冲突，实际 %v", err)
	}
	// 重新打开能读到刚才写入的数据
	s2, err := OpenStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(s2.Snapshot().Groups); got != 2 {
		t.Fatalf("重新打开后应有 2 个分组，实际 %d", got)
	}
}

func TestSniffIcon(t *testing.T) {
	cases := map[string]string{
		"\x89PNG\r\n\x1a\nxxxx":                       "png",
		"\xef\xbb\xbf  <?xml version=\"1.0\"?><svg/>": "svg",
		"<svg xmlns='http://www.w3.org/2000/svg'/>":   "svg",
		"<!doctype html><html><svg></svg></html>":     "",
		"hello": "",
	}
	for in, want := range cases {
		if got := sniffIcon([]byte(in)); got != want {
			t.Errorf("sniffIcon(%q) = %q，期望 %q", strings.ToValidUTF8(in, "?"), got, want)
		}
	}
}

func TestDisplayHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"http://192.168.1.10:8080":   "192.168.1.10:8080",
		"http://192.168.1.1:9090/ui": "192.168.1.1:9090/ui",
		"https://github.com/":        "github.com",
	} {
		if got := displayHost(in); got != want {
			t.Errorf("displayHost(%q) = %q，期望 %q", in, got, want)
		}
	}
	if initial(" pve") != "P" || initial("路由器") != "路" || initial("") != "?" {
		t.Fatal("initial 结果不对")
	}
}

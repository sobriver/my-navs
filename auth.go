package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Role int

const (
	RoleNone  Role = iota
	RoleUser       // 只能看导航页
	RoleAdmin      // 导航页 + 后台
)

const cookieName = "navs_session"

type Auth struct {
	secret     []byte
	pwHash     map[Role][32]byte
	ttl        time.Duration
	trustProxy bool
	limiter    *limiter
}

func NewAuth(cfg *Config, secret []byte) *Auth {
	return &Auth{
		secret: secret,
		pwHash: map[Role][32]byte{
			RoleUser:  sha256.Sum256([]byte(cfg.NavPassword)),
			RoleAdmin: sha256.Sum256([]byte(cfg.AdminPassword)),
		},
		ttl:        cfg.SessionTTL,
		trustProxy: cfg.TrustProxy,
		limiter:    newLimiter(),
	}
}

// loadSecret 读取会话签名密钥，不存在就生成一个。密钥持久化后容器重启不会掉登录。
func loadSecret(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil && len(b) == 32 {
		return b, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	b = make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, b); err != nil {
		return nil, err
	}
	return b, nil
}

// Check 比较密码，返回对应的角色。两个密码相同时视为管理员。
func (a *Auth) Check(pw string) Role {
	h := sha256.Sum256([]byte(pw))
	admin, user := a.pwHash[RoleAdmin], a.pwHash[RoleUser]
	isAdmin := subtle.ConstantTimeCompare(h[:], admin[:]) == 1
	isUser := subtle.ConstantTimeCompare(h[:], user[:]) == 1
	switch {
	case isAdmin:
		return RoleAdmin
	case isUser:
		return RoleUser
	}
	return RoleNone
}

// 签名里混入了对应角色的密码哈希，改密码后旧会话自然失效。
func (a *Auth) mac(r Role, payload string) string {
	m := hmac.New(sha256.New, a.secret)
	pw := a.pwHash[r]
	m.Write(pw[:])
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Issue 生成会话令牌，格式为 角色.过期时间.签名。
func (a *Auth) Issue(r Role, now time.Time) string {
	payload := fmt.Sprintf("%d.%d", r, now.Add(a.ttl).Unix())
	return payload + "." + a.mac(r, payload)
}

func (a *Auth) Verify(tok string, now time.Time) (Role, time.Time) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return RoleNone, time.Time{}
	}
	n, err := strconv.Atoi(parts[0])
	r := Role(n)
	if err != nil || (r != RoleUser && r != RoleAdmin) {
		return RoleNone, time.Time{}
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return RoleNone, time.Time{}
	}
	expAt := time.Unix(exp, 0)
	if !now.Before(expAt) {
		return RoleNone, time.Time{}
	}
	want := a.mac(r, parts[0]+"."+parts[1])
	if !hmac.Equal([]byte(parts[2]), []byte(want)) {
		return RoleNone, time.Time{}
	}
	return r, expAt
}

func (a *Auth) Session(r *http.Request) (Role, time.Time) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return RoleNone, time.Time{}
	}
	return a.Verify(c.Value, time.Now())
}

func (a *Auth) SetCookie(w http.ResponseWriter, r *http.Request, role Role) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    a.Issue(role, time.Now()),
		Path:     "/",
		MaxAge:   int(a.ttl.Seconds()),
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *Auth) ClearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// ClientIP 用于登录限流。只有明确信任反代时才读转发头，否则可以被伪造绕过限流。
func (a *Auth) ClientIP(r *http.Request) string {
	if a.trustProxy {
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

const (
	maxFails     = 5
	lockDuration = 15 * time.Minute
	maxTracked   = 10000
)

// limiter 记录每个 IP 的登录失败次数，连续失败 maxFails 次后锁定 lockDuration。
type limiter struct {
	mu sync.Mutex
	m  map[string]*attempt
}

type attempt struct {
	fails int
	last  time.Time
	until time.Time
}

func newLimiter() *limiter {
	return &limiter{m: make(map[string]*attempt)}
}

func (l *limiter) Blocked(ip string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if at := l.m[ip]; at != nil && now.Before(at.until) {
		return at.until.Sub(now)
	}
	return 0
}

// Fail 记一次失败，如果因此被锁定则返回锁定时长。
func (l *limiter) Fail(ip string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	at := l.m[ip]
	if at == nil || now.Sub(at.last) > lockDuration {
		if len(l.m) >= maxTracked {
			l.prune(now)
		}
		at = &attempt{}
		l.m[ip] = at
	}
	at.fails++
	at.last = now
	if at.fails >= maxFails {
		at.fails = 0
		at.until = now.Add(lockDuration)
		return lockDuration
	}
	return 0
}

func (l *limiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, ip)
}

func (l *limiter) prune(now time.Time) {
	for ip, at := range l.m {
		if now.Sub(at.last) > lockDuration && !now.Before(at.until) {
			delete(l.m, ip)
		}
	}
}

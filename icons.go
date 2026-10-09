package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxIconSize = 512 << 10

// 图标文件按内容哈希命名，同一张图只存一份，也可以放心地长期缓存。
var iconPattern = regexp.MustCompile(`^[0-9a-f]{32}\.(png|jpg|gif|webp|ico|svg)$`)

var iconTypes = map[string]string{
	"png":  "image/png",
	"jpg":  "image/jpeg",
	"gif":  "image/gif",
	"webp": "image/webp",
	"ico":  "image/x-icon",
	"svg":  "image/svg+xml",
}

var errBadImage = errors.New("不是支持的图片格式（png / jpg / gif / webp / ico / svg）")

// remoteStatusError 表示连上了远程服务器但它返回了非 200，和网络不通区分开。
type remoteStatusError int

func (e remoteStatusError) Error() string {
	return fmt.Sprintf("下载失败：远程返回 %d", int(e))
}

type Icons struct {
	dir    string
	client *http.Client
}

func NewIcons(dir string) *Icons {
	// 默认 Transport 会读取 HTTPS_PROXY 等环境变量，服务器访问不了外网时可以走代理
	return &Icons{dir: dir, client: &http.Client{Timeout: 15 * time.Second}}
}

// sniffIcon 根据文件内容判断图片类型，不信任扩展名和 Content-Type。
func sniffIcon(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		return "jpg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return "gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "webp"
	case bytes.HasPrefix(b, []byte{0, 0, 1, 0}):
		return "ico"
	}
	head := b
	if len(head) > 4096 {
		head = head[:4096]
	}
	head = bytes.ToLower(bytes.TrimSpace(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf"))))
	if bytes.HasPrefix(head, []byte("<")) && bytes.Contains(head, []byte("<svg")) && !bytes.Contains(head, []byte("<html")) {
		return "svg"
	}
	return ""
}

func (ic *Icons) Save(b []byte) (string, error) {
	if len(b) == 0 {
		return "", errors.New("图片是空的")
	}
	if len(b) > maxIconSize {
		return "", errors.New("图片不能超过 512KB")
	}
	ext := sniffIcon(b)
	if ext == "" {
		return "", errBadImage
	}
	sum := sha256.Sum256(b)
	name := hex.EncodeToString(sum[:16]) + "." + ext
	path := filepath.Join(ic.dir, name)
	if _, err := os.Stat(path); err == nil {
		// 刷新修改时间，避免还没保存到链接上就被 GC 清掉
		now := time.Now()
		os.Chtimes(path, now, now)
		return name, nil
	}
	if err := writeFileAtomic(path, b); err != nil {
		return "", err
	}
	return name, nil
}

func (ic *Icons) Fetch(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("图片地址无效")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; my-navs)")
	req.Header.Set("Accept", "image/*,*/*;q=0.8")
	resp, err := ic.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", remoteStatusError(resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxIconSize+1))
	if err != nil {
		return "", fmt.Errorf("下载失败：%v", err)
	}
	return ic.Save(b)
}

func (ic *Icons) Read(name string) ([]byte, error) {
	if !iconPattern.MatchString(name) {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(filepath.Join(ic.dir, name))
}

func (ic *Icons) Serve(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !iconPattern.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(ic.dir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", iconTypes[name[strings.LastIndexByte(name, '.')+1:]])
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	// 直接打开 SVG 时禁止其中的脚本执行
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// GC 删除没有被任何链接引用、且超过一小时没动过的图标文件。
func (ic *Icons) GC(keep map[string]bool) {
	entries, err := os.ReadDir(ic.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-time.Hour)
	for _, e := range entries {
		name := e.Name()
		if keep[name] || !iconPattern.MatchString(name) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		os.Remove(filepath.Join(ic.dir, name))
	}
}

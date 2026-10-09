package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Link struct {
	ID      string `json:"id"`
	GroupID string `json:"groupId"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Icon    string `json:"icon,omitempty"`
	Desc    string `json:"desc,omitempty"`
	SameTab bool   `json:"sameTab,omitempty"` // 默认在新标签页打开
}

// Data 是全部导航数据。分组和链接的顺序就是切片里的顺序。
type Data struct {
	Version int     `json:"version"`
	Groups  []Group `json:"groups"`
	Links   []Link  `json:"links"`
}

const (
	maxGroups = 100
	maxLinks  = 2000
)

var (
	ErrConflict = errors.New("数据已在别处修改，请刷新后重试")
	idPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
)

// invalidError 表示提交的数据不合法，消息可以直接展示给用户。
type invalidError struct{ msg string }

func (e *invalidError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &invalidError{fmt.Sprintf(format, args...)}
}

func (d *Data) normalize() error {
	if d.Groups == nil {
		d.Groups = []Group{}
	}
	if d.Links == nil {
		d.Links = []Link{}
	}
	if len(d.Groups) > maxGroups {
		return invalid("分组不能超过 %d 个", maxGroups)
	}
	if len(d.Links) > maxLinks {
		return invalid("链接不能超过 %d 个", maxLinks)
	}

	groups := make(map[string]bool, len(d.Groups))
	for i := range d.Groups {
		g := &d.Groups[i]
		g.Name = strings.TrimSpace(g.Name)
		if !idPattern.MatchString(g.ID) || groups[g.ID] {
			return invalid("分组 ID 无效或重复：%q", g.ID)
		}
		if g.Name == "" || utf8.RuneCountInString(g.Name) > 32 {
			return invalid("分组名称不能为空，且不能超过 32 个字")
		}
		groups[g.ID] = true
	}

	links := make(map[string]bool, len(d.Links))
	for i := range d.Links {
		l := &d.Links[i]
		l.Name = strings.TrimSpace(l.Name)
		l.URL = strings.TrimSpace(l.URL)
		l.Desc = strings.TrimSpace(l.Desc)
		if !idPattern.MatchString(l.ID) || links[l.ID] {
			return invalid("链接 ID 无效或重复：%q", l.ID)
		}
		links[l.ID] = true
		if l.Name == "" || utf8.RuneCountInString(l.Name) > 64 {
			return invalid("链接名称不能为空，且不能超过 64 个字")
		}
		if !groups[l.GroupID] {
			return invalid("链接「%s」所属的分组不存在", l.Name)
		}
		if len(l.URL) > 2048 || !validURL(l.URL) {
			return invalid("链接「%s」的地址无效，需要以 http:// 或 https:// 开头", l.Name)
		}
		if utf8.RuneCountInString(l.Desc) > 200 {
			return invalid("链接「%s」的备注不能超过 200 个字", l.Name)
		}
		if l.Icon != "" && !iconPattern.MatchString(l.Icon) {
			return invalid("链接「%s」的图标无效", l.Name)
		}
	}
	return nil
}

func validURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// IconSet 返回所有被引用的图标文件名。
func (d *Data) IconSet() map[string]bool {
	set := make(map[string]bool)
	for _, l := range d.Links {
		if l.Icon != "" {
			set[l.Icon] = true
		}
	}
	return set
}

type Store struct {
	mu   sync.RWMutex
	path string
	data Data
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s.data = Data{Version: 1, Groups: []Group{{ID: "home", Name: "Home"}}, Links: []Link{}}
		return s, s.write(s.data)
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, fmt.Errorf("解析 %s 失败：%w", path, err)
	}
	if err := s.data.normalize(); err != nil {
		return nil, fmt.Errorf("%s 内容不合法：%w", path, err)
	}
	if s.data.Version < 1 {
		s.data.Version = 1
	}
	return s, nil
}

func (s *Store) Snapshot() Data {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Data{
		Version: s.data.Version,
		Groups:  slices.Clone(s.data.Groups),
		Links:   slices.Clone(s.data.Links),
	}
}

// Replace 整体替换数据。expect 是客户端拿到的版本号，不一致说明期间被别处改过；
// 传负数则跳过检查（导入时用）。
func (s *Store) Replace(d Data, expect int) (Data, error) {
	if err := d.normalize(); err != nil {
		return Data{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if expect >= 0 && expect != s.data.Version {
		return Data{}, ErrConflict
	}
	d.Version = s.data.Version + 1
	if err := s.write(d); err != nil {
		return Data{}, err
	}
	s.data = d
	return Data{Version: d.Version, Groups: slices.Clone(d.Groups), Links: slices.Clone(d.Links)}, nil
}

func (s *Store) write(d Data) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, b)
}

// writeFileAtomic 先写临时文件再改名，避免写到一半断电把文件写坏。
func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

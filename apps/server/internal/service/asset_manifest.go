package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ManifestEntry 是清单摘要计算与全量暂存用的最小条目（与 agent 上报 upserts 同形，见规格 §4.3）。
type ManifestEntry struct {
	Path    string
	SHA256  string
	Size    int64
	MtimeMs int64
	IsText  bool
}

// manifestDigestSep 是摘要拼接分隔符（每字段一行，与 agent 端 AssetManifestDigest 同源）。
var manifestDigestSep = []byte{'\n'}

// computeManifestDigest 计算清单摘要（规格 §4.3）：条目按 path 字节序升序，
// 逐条拼接 `path\nsha256\nsize\nmtime_ms\n`，对整体 UTF-8 取 sha256 小写 hex。
// agent 与控制面同算法，摘要相等即两侧清单一致。纯函数、无副作用（不改入参切片顺序）。
func computeManifestDigest(entries []ManifestEntry) string {
	sorted := make([]ManifestEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for i := range sorted {
		e := &sorted[i]
		_, _ = io.WriteString(h, e.Path)
		_, _ = h.Write(manifestDigestSep)
		_, _ = io.WriteString(h, e.SHA256)
		_, _ = h.Write(manifestDigestSep)
		_, _ = io.WriteString(h, strconv.FormatInt(e.Size, 10))
		_, _ = h.Write(manifestDigestSep)
		_, _ = io.WriteString(h, strconv.FormatInt(e.MtimeMs, 10))
		_, _ = h.Write(manifestDigestSep)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// maxExtLen 是扩展名列（ext VARCHAR(16)）的截断上限，防超长扩展名撑破列。
const maxExtLen = 16

// deriveExt 从相对路径推导小写扩展名（无扩展名 / dotfile 返回空串），冗余落 file_asset.ext 支撑扩展名搜索。
func deriveExt(p string) string {
	base := p
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 { // 无点 或 dotfile（如 .env，点在首位视作无扩展名）
		return ""
	}
	ext := strings.ToLower(base[dot+1:])
	if len(ext) > maxExtLen {
		ext = ext[:maxExtLen]
	}
	return ext
}

// maxPathLen 是相对路径列（path VARCHAR(512)）的上限。
const maxPathLen = 512

// sha256HexLen 是 sha256 小写 hex 的定长。
const sha256HexLen = 64

// validAssetPath 校验相对路径安全性（防越权 / 撑破列）：非空、≤512、不以 / 开头、无反斜杠、无空 / . / .. 段。
func validAssetPath(p string) bool {
	if p == "" || len(p) > maxPathLen {
		return false
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// validSHA256Hex 校验 sha256 为 64 位小写十六进制。
func validSHA256Hex(s string) bool {
	if len(s) != sha256HexLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// errStagingOutOfSync 是全量分片暂存基线丢失 / 乱序 / 过期的哨兵，service 映射为 ErrAssetManifestOutOfSync。
var errStagingOutOfSync = errors.New("全量分片暂存失配")

// fullUploadStaging 是全量上报的进程内分片暂存（规格 §4.3）：按 uploadId 键累积分片，
// TTL 内收齐（eof）后一次性交付、清除。非 Redis / MQ，纯进程内 map + 锁（不违反架构不变量 §2）。
type fullUploadStaging struct {
	mu      sync.Mutex
	entries map[string]*stagingEntry
	ttl     time.Duration
	now     func() time.Time
}

// stagingEntry 是单个 uploadId 的累积态。
type stagingEntry struct {
	nextSeq   int
	upserts   []ManifestEntry
	startedAt time.Time
}

// newFullUploadStaging 构造暂存（ttl 为分片保活窗口，now 供测试注入时钟）。
func newFullUploadStaging(ttl time.Duration, now func() time.Time) *fullUploadStaging {
	return &fullUploadStaging{entries: map[string]*stagingEntry{}, ttl: ttl, now: now}
}

// append 累积一个全量分片。语义（规格 §4.3）：
//   - seq==0 新建（覆盖同 key 旧态，agent 重启新一轮全量）；
//   - seq>0 须匹配已暂存的 nextSeq，否则视为乱序 / 基线丢失 → errStagingOutOfSync；
//   - eof=true 时返回收齐的全部条目 + assembled=true 并清除暂存；否则 assembled=false。
//
// 每次调用先清过期条目，避免放弃的上传长期滞留内存。
func (s *fullUploadStaging) append(key string, seq int, eof bool, batch []ManifestEntry) (assembled []ManifestEntry, done bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()

	if seq == 0 {
		s.entries[key] = &stagingEntry{nextSeq: 1, upserts: append([]ManifestEntry(nil), batch...), startedAt: s.now()}
	} else {
		e, ok := s.entries[key]
		if !ok || e.nextSeq != seq {
			delete(s.entries, key)
			return nil, false, errStagingOutOfSync
		}
		e.upserts = append(e.upserts, batch...)
		e.nextSeq = seq + 1
	}
	if !eof {
		return nil, false, nil
	}
	e := s.entries[key]
	delete(s.entries, key)
	return e.upserts, true, nil
}

// purgeExpiredLocked 清除超 TTL 的未收齐上传（调用方须持锁）。
func (s *fullUploadStaging) purgeExpiredLocked() {
	cutoff := s.now().Add(-s.ttl)
	for k, e := range s.entries {
		if e.startedAt.Before(cutoff) {
			delete(s.entries, k)
		}
	}
}

// stagingKey 组合服 id 与 uploadId 为暂存键（防跨服 uploadId 碰撞）。
func stagingKey(serverID uint, uploadID string) string {
	return strconv.FormatUint(uint64(serverID), 10) + ":" + uploadID
}

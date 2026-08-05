package authz

import (
	"encoding/hex"
	"strings"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
)

// SensitiveAccessTarget 绑定敏感读取审批中的稳定资源引用与内容版本哈希。
type SensitiveAccessTarget struct {
	Ref         string
	ContentHash string
}

// NewSensitiveAccessTarget 冻结授权目标；引用不得包含原始敏感内容或不稳定展示文本。
//
// 各领域以“资源类型/稳定标识”传入：消息使用 message/<消息ID>，配置和文件应带版本标识，
// 预览或 diff 使用资源稳定标识和规范化内容元组的 SHA-256，不能把敏感路径或明文放入引用。
func NewSensitiveAccessTarget(resource, stableRef, contentHash string) (SensitiveAccessTarget, error) {
	if !stableSensitiveAccessPart(resource) || !stableSensitiveAccessPart(stableRef) || !validSensitiveContentHash(contentHash) {
		return SensitiveAccessTarget{}, apperr.ErrInvalidParam
	}
	return SensitiveAccessTarget{Ref: resource + "/" + stableRef, ContentHash: contentHash}, nil
}

func stableSensitiveAccessPart(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, " \\\r\n\t") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func validSensitiveContentHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

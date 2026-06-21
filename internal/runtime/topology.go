package runtime

import (
	"sort"
	"strings"

	"github.com/wcpe/Beacon/internal/merge"
)

// TopologyDigest 计算一组实例的拓扑摘要（纯函数，供拓扑 watch"真变才推"去重）。
//
// 只把拓扑相关字段纳入摘要：serverId / role / group / zone / status / address；
// 在线人数、tps、心跳时间、版本等运行指标不入摘要，避免无谓推送。
// 按 serverId 字典序拼接后取 md5，保证入参顺序不影响结果（与 merge.OverallMD5 同口径）。
func TopologyDigest(insts []*Instance) string {
	lines := make([]string, 0, len(insts))
	for _, i := range insts {
		lines = append(lines, i.ServerID+"|"+i.Role+"|"+i.ResolvedGroup+"|"+i.ResolvedZone+"|"+i.Status+"|"+i.Address)
	}
	sort.Strings(lines)
	return merge.MD5Hex(strings.Join(lines, "\n"))
}

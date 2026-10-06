package service

import "github.com/wcpe/Beacon/apps/server/internal/runtime"

// RegistryDeclarationLabels 把运行期注册表接到调度决策的「自声明标签」读取真源上（FR-244）。
//
// 它是**只读**适配：读的是 FR-243 声明端点写进注册表的那一份（`Instance.Metadata`），
// 不另建标签表、也不另存一份标签真源——决策看到的就是节点自己声明的东西
// （`Registry.Get` 本身已对实例做深拷贝，调用方拿到的不是注册表的可变引用）。
//
// 为什么不经健康视图：健康视图的事实来自 DB 投影（FR-147），而自声明按 FR-243 / ADR-0086 是
// **进程内存事实、不落 DB**；两者不是同一处，故在这里做一次并读，而不是把声明塞进健康计算的输入
// （那会让"声明"变成打分因子——本仓明确不做）。
//
// 并读的代价如实登记：候选与标签之间**没有共享快照**，若某台节点在两读之间恰好刷新了声明，
// 这一次决策可能用的是旧标签。它只影响"这一帧"，下一次请求即纠正；而为了消除它去锁住注册表整轮
// 决策（决策目标 <5ms、请求 goroutine 零阻塞）代价更大，故按现状不取。
type RegistryDeclarationLabels struct {
	Registry *runtime.Registry
}

// DeclaredLabels 返回某节点当前自声明的键值标签（namespace 为环境 code）。
// 未装配注册表 / 节点不在册 / 无标签一律返回 **nil** —— 这三件事在准入判定上同效
// （都不满足非空作用域），故不在这里区分；候选快照侧的"非 nil 空 map = 该节点没声明过"
// 由 schedCandidateOf 拷贝时补出，与"看不到声明"（nil）保持可区分。
func (r RegistryDeclarationLabels) DeclaredLabels(namespace string, serverID string) map[string]string {
	if r.Registry == nil {
		return nil
	}
	inst := r.Registry.Get(namespace, serverID)
	if inst == nil || len(inst.Metadata) == 0 {
		return nil
	}
	return inst.Metadata
}

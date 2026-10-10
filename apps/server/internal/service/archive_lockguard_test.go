package service

// —— P1-3 判别性用例：归档服务的锁内 DB 已纳入守卫观测面 ——
//
// 背景（独立评审 P1-3）：守卫的省略理由曾写「其余锁（observeMu / stallMu 等）是叶子锁，
// 其持有期间不做 DB 访问」，但归档服务的 `s.mu` 下**确实**有 DB（CreateJob 的 HasActiveJob、
// RetryJob / CancelJob 的 GetJob，以及前者的 hotDB.Transaction）。该锁当时是普通 sync.Mutex、
// 不受 lockguard 观测，于是：
//   - 「持锁做 DB 一律被观测」这一声称在第二处服务上不成立；
//   - 反模式留在代码里而无人可见。
//
// 处置取评审给出的 B 路径（观测扩面）+ 订正省略理由，理由见 ArchiveService.createJobInternal
// 与 mu 字段说明：单飞判据没有 DB 兜底（活跃集是条件集合，两种方言都写不出部分唯一索引），
// 故方案 A 的 CAS 定序前提不成立，强改会把「至多一个活跃任务」降级成「通常一个」。
//
// 判别力靠**先证有牙、再判命中、再证不误报**三段，缺一段都无法排除假绿：
//   - 有牙：观测开启时，服务自己的「持锁查库」必须被抓到；
//   - 命中：真实入口 CreateJob / CancelJob 的锁内 DB 也必须在计数里体现；
//   - 不误报：观测关闭时（生产默认）计数归零——守卫是诊断件，不能在生产常态下污染日志。

import (
	"errors"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/pkg/lockguard"
)

// TestArchiveLockDBGuardObservesServiceMutex 断言归档服务的锁内 DB 会被守卫捕获。
//
// 为什么断言的是「捕获到违规」而不是「零违规」：归档这三处锁内 DB 经评审确认**保留**
// （理由见 createJobInternal），所以本用例要证明的恰恰是「守卫看得见它们」——
// 若改成断言零违规，就把一个有意保留的设计判成缺陷，且会在有人按方案 A 误改后假绿。
func TestArchiveLockDBGuardObservesServiceMutex(t *testing.T) {
	svc, hot, _ := newArchiveTestService(t)
	seedMetricDailyTable(t, hot, archiveTestNow.AddDate(0, 0, -30), 1)

	// —— 装配后：句柄必须非空 ——
	// 为什么这条要显式断言：AttachLockDBGuard 内部失败只记 WARN、不报错（诊断件不该阻断启动），
	// 故「守卫是否真的挂上了」没有别的可核验依据。
	svc.AttachLockDBGuard()
	if svc.lockGuard == nil {
		t.Fatal("守卫未挂上（AttachLockDBGuard 内部失败只记 WARN、不报错，故须在此显式断言）")
	}
	lockguard.SetEnabled(true)
	t.Cleanup(func() { lockguard.SetEnabled(false) })

	// —— 第一段：有牙 ——
	// 直接以服务自己的锁做一次「持锁查库」；守卫若真在观测这把锁，这必然是违规。
	svc.mu.Lock()
	var n int64
	err := hot.Model(&model.ArchiveJob{}).Count(&n).Error
	svc.mu.Unlock()
	if err != nil {
		t.Fatalf("持锁查询失败: %v", err)
	}
	if v := svc.lockGuard.Violations(); v == 0 {
		t.Fatal("守卫没有牙：持 svc.mu 期间发起 DB 访问未被捕获——观测面未覆盖该锁（P1-3 回归）")
	}
	if svc.lockGuard.Accesses() == 0 {
		t.Fatal("判据空洞：守卫计不到任何 DB 访问，后续零违规/命中数都不可信")
	}

	// —— 第二段：真实入口命中 ——
	// 重置计数后跑 CreateJob（含 HasActiveJob + hotDB.Transaction）与 CancelJob（含 GetJob）。
	svc.lockGuard.Reset()
	if _, err := svc.CreateJob(model.ArchiveModeDryRun, []string{"metric_sample"}, "admin"); err != nil {
		t.Fatalf("创建任务失败——本用例需要 CreateJob 走完锁内 DB 路径: %v", err)
	}
	if _, err := svc.CancelJob(1, "admin"); err != nil && !errors.Is(err, apperr.ErrArchiveJobNotFound) &&
		!errors.Is(err, apperr.ErrArchiveJobState) {
		t.Fatalf("取消任务失败: %v", err)
	}
	if v := svc.lockGuard.Violations(); v == 0 {
		t.Fatal("CreateJob / CancelJob 的锁内 DB 未被观测到——守卫虽已挂上，却漏了这两条路径")
	}
	t.Logf("观测面生效：CreateJob / CancelJob 期间捕获 %d 次「持锁做 DB」违规（首个调用点 %s）",
		svc.lockGuard.Violations(), svc.lockGuard.Site())

	// —— 第三段：不误报（生产默认关时不计数）——
	// 守卫是诊断件：关闭状态下必须完全静默，否则会把生产日志变成噪声。
	lockguard.SetEnabled(false)
	svc.lockGuard.Reset()
	svc.mu.Lock()
	_ = hot.Model(&model.ArchiveJob{}).Count(&n).Error
	svc.mu.Unlock()
	if v := svc.lockGuard.Violations(); v != 0 {
		t.Fatalf("观测关闭时仍记违规 %d 次——守卫须在生产默认态零开销、零噪声", v)
	}
}

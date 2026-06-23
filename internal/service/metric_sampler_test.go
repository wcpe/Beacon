package service

import (
	"context"
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/runtime"
)

// fakeMetricSink 是 metricSink 的测试替身：记录批量插入与清理调用，验证采样器调用时序与参数。
type fakeMetricSink struct {
	insertedBatches [][]model.MetricSample // 每次 InsertBatch 的入参快照
	deleteCutoffs   []time.Time            // 每次 DeleteBefore 的 cutoff
	deleteReturn    int64
}

func (f *fakeMetricSink) InsertBatch(samples []model.MetricSample) error {
	// 拷贝一份避免上层复用切片造成串扰。
	cp := make([]model.MetricSample, len(samples))
	copy(cp, samples)
	f.insertedBatches = append(f.insertedBatches, cp)
	return nil
}

func (f *fakeMetricSink) DeleteBefore(cutoff time.Time) (int64, error) {
	f.deleteCutoffs = append(f.deleteCutoffs, cutoff)
	return f.deleteReturn, nil
}

// TestSampleOnceSnapshotToBatch 验证一轮采样：从注册表取在线快照 → 转样本 → 批量插入参数正确。
func TestSampleOnceSnapshotToBatch(t *testing.T) {
	reg := runtime.NewRegistry()
	now := time.Now().UTC()
	mustRegister(t, reg, "prod", "lobby-1", "10.0.0.1:25565", now)
	mustRegister(t, reg, "prod", "lobby-2", "10.0.0.2:25565", now)
	reg.Report("prod", "lobby-1", "m", 42, 19.9, 128, 512, 0.3, nil)
	reg.Report("prod", "lobby-2", "m", 7, 20.0, 64, 256, -1.0, nil)

	sink := &fakeMetricSink{}
	sampler := NewMetricSampler(reg, sink, settingsWith(t, nil))

	at := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	n := sampler.sampleOnce(at)
	if n != 2 {
		t.Fatalf("应采样 2 个在线实例，实际 %d", n)
	}
	if len(sink.insertedBatches) != 1 {
		t.Fatalf("应批量插入 1 次，实际 %d", len(sink.insertedBatches))
	}
	batch := sink.insertedBatches[0]
	if len(batch) != 2 {
		t.Fatalf("批量应含 2 条样本，实际 %d", len(batch))
	}
	byServer := map[string]model.MetricSample{}
	for _, s := range batch {
		byServer[s.ServerID] = s
		if !s.SampledAt.Equal(at) {
			t.Fatalf("样本 sampledAt 应为采样时刻 %v，实际 %v", at, s.SampledAt)
		}
		if s.Namespace != "prod" {
			t.Fatalf("样本 namespace 错误：%v", s.Namespace)
		}
	}
	s1 := byServer["lobby-1"]
	if s1.PlayerCount != 42 || s1.TPS != 19.9 || s1.MemUsed != 128 || s1.MemMax != 512 || s1.CPULoad != 0.3 {
		t.Fatalf("lobby-1 样本字段错误：%+v", s1)
	}
	// 角色从 registry 落库（趋势降采样据此排除 bungee 出平均 TPS·CPU）。
	if s1.Role != "bukkit" {
		t.Fatalf("lobby-1 样本角色应从 registry 落库为 bukkit，实际 %v", s1.Role)
	}
	if byServer["lobby-2"].CPULoad != -1.0 {
		t.Fatalf("lobby-2 不可用 CPU 哨兵应原样落样本，实际 %v", byServer["lobby-2"].CPULoad)
	}
}

// TestSampleOnceEmptyNoInsert 无在线实例时不发批量插入（空批安全略过）。
func TestSampleOnceEmptyNoInsert(t *testing.T) {
	reg := runtime.NewRegistry()
	sink := &fakeMetricSink{}
	sampler := NewMetricSampler(reg, sink, settingsWith(t, nil))
	if n := sampler.sampleOnce(time.Now().UTC()); n != 0 {
		t.Fatalf("无在线实例应采样 0，实际 %d", n)
	}
	if len(sink.insertedBatches) != 0 {
		t.Fatalf("无在线实例不应触发插入，实际 %d 次", len(sink.insertedBatches))
	}
}

// TestSampleOnceExcludesNonOnline 仅采样在线/亚健康（degraded）实例不在本采样集合内的验证：
// 这里聚焦“只采在线”——lost/offline 不进样本。
func TestSampleOnceExcludesNonOnline(t *testing.T) {
	const (
		degradedAfter = 15 * time.Second
		ttl           = 30 * time.Second
		offlineGrace  = 120 * time.Second
	)
	reg := runtime.NewRegistry()
	t0 := time.Now().UTC()
	mustRegister(t, reg, "prod", "fresh", "10.0.0.1:25565", t0)
	mustRegister(t, reg, "prod", "stale", "10.0.0.2:25565", t0)

	// fresh 续心跳保持 online；stale 停在 t0，推进到 lost。
	t1 := t0.Add(ttl + time.Second)
	reg.Heartbeat("prod", "fresh", t1)
	reg.SweepExpired(t1, degradedAfter, ttl, offlineGrace)

	sink := &fakeMetricSink{}
	sampler := NewMetricSampler(reg, sink, settingsWith(t, nil))
	n := sampler.sampleOnce(t1)
	if n != 1 {
		t.Fatalf("仅在线实例应被采样（lost 排除），实际采样 %d", n)
	}
	if sink.insertedBatches[0][0].ServerID != "fresh" {
		t.Fatalf("被采样的应为在线的 fresh，实际 %v", sink.insertedBatches[0][0].ServerID)
	}
}

// TestRetentionDeletesBeforeCutoff 验证保留期清理按 now-保留期 算 cutoff 调 DeleteBefore。
func TestRetentionDeletesBeforeCutoff(t *testing.T) {
	reg := runtime.NewRegistry()
	sink := &fakeMetricSink{deleteReturn: 5}
	retention := 24 * time.Hour
	sampler := NewMetricSampler(reg, sink, settingsWith(t, nil))

	now := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	sampler.cleanupOnce(now, retention)
	if len(sink.deleteCutoffs) != 1 {
		t.Fatalf("应触发 1 次清理，实际 %d", len(sink.deleteCutoffs))
	}
	wantCutoff := now.Add(-retention)
	if !sink.deleteCutoffs[0].Equal(wantCutoff) {
		t.Fatalf("清理 cutoff 应为 now-保留期=%v，实际 %v", wantCutoff, sink.deleteCutoffs[0])
	}
}

// TestRunSkipsSamplingWhenDisabled 守护 FR-61：metric.enabled=false 时 Run 每轮跳过采样 / 清理（不重启停采样）。
// 采样间隔 1s（ticker 最小粒度），enabled=false，跑 ~1.5s 至少一轮，sink 不应有任何写入。
func TestRunSkipsSamplingWhenDisabled(t *testing.T) {
	reg := runtime.NewRegistry()
	mustRegister(t, reg, "prod", "lobby-1", "10.0.0.1:25565", time.Now().UTC())
	sink := &fakeMetricSink{}
	// metric.enabled=false、间隔 1s、保留期 1h。
	settings := settingsWith(t, map[string]string{
		SettingMetricEnabled:           "false",
		SettingMetricSampleIntervalSec: "1",
		SettingMetricRetentionHours:    "1",
	})
	sampler := NewMetricSampler(reg, sink, settings)

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	sampler.Run(ctx)

	if len(sink.insertedBatches) != 0 {
		t.Fatalf("enabled=false 时不应采样落库，实际写入 %d 次", len(sink.insertedBatches))
	}
	if len(sink.deleteCutoffs) != 0 {
		t.Fatalf("enabled=false 时不应清理，实际清理 %d 次", len(sink.deleteCutoffs))
	}
}

// TestRunSamplesWhenEnabled 守护 FR-61：metric.enabled=true 时 Run 按间隔采样落库（与 disabled 对照）。
func TestRunSamplesWhenEnabled(t *testing.T) {
	reg := runtime.NewRegistry()
	mustRegister(t, reg, "prod", "lobby-1", "10.0.0.1:25565", time.Now().UTC())
	sink := &fakeMetricSink{}
	settings := settingsWith(t, map[string]string{
		SettingMetricEnabled:           "true",
		SettingMetricSampleIntervalSec: "1",
		SettingMetricRetentionHours:    "1",
	})
	sampler := NewMetricSampler(reg, sink, settings)

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	sampler.Run(ctx)

	if len(sink.insertedBatches) == 0 {
		t.Fatal("enabled=true 时应至少采样落库 1 次，实际 0 次")
	}
}

// mustRegister 注册一个在线实例（测试辅助）。
func mustRegister(t *testing.T, reg *runtime.Registry, ns, serverID, addr string, now time.Time) {
	t.Helper()
	_, err := reg.Register(&runtime.Instance{
		Namespace: ns, ServerID: serverID, Role: "bukkit", Address: addr,
	}, 30*time.Second, now)
	if err != nil {
		t.Fatalf("注册 %s 失败: %v", serverID, err)
	}
}

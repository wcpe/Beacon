package service

import (
	"errors"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

func TestDangerousSettingApplyUsesFrozenVersionCAS(t *testing.T) {
	svc, db := newTestSettingsService(t)
	item := model.Setting{Key: SettingHealthTTLSec, Value: "30", ValueType: model.SettingValueTypeInt, Version: 1}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("写初始设置失败: %v", err)
	}
	var after func()
	if err := db.Transaction(func(tx *gorm.DB) error {
		callback, err := svc.applyDangerousInTx(tx, item.Key, "45", item.Version, "tester", "")
		after = callback
		return err
	}); err != nil {
		t.Fatalf("冻结版本未漂移应成功: %v", err)
	}
	after()
	var saved model.Setting
	if err := db.Where("setting_key = ?", item.Key).First(&saved).Error; err != nil || saved.Value != "45" || saved.Version != 2 {
		t.Fatalf("CAS 写入不正确: %+v, %v", saved, err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := svc.applyDangerousInTx(tx, item.Key, "60", item.Version, "tester", "")
		return err
	}); err == nil {
		t.Fatal("设置版本漂移必须失败")
	}
	if err := db.Where("setting_key = ?", item.Key).First(&saved).Error; err != nil {
		t.Fatalf("读取 CAS 冲突后的设置失败: %v", err)
	}
	if saved.Value != "45" || saved.Version != 2 {
		t.Fatalf("CAS 冲突不得覆盖已提交的设置，实际 %+v", saved)
	}
}

// TestRequestUpdateDistinguishesKeyErrors 验证高影响设置提审入口对「key 不存在」与
// 「key 存在但非高影响项」给出可区分的 400，而不是统一的泛化 403。
//
// 动机：该入口由 MCP 工具 beacon.system.settings.update-dangerous 与 HTTP 设置端点共用，
// key 完全由调用方传入。此前两类都报 ErrForbidden（「只读密钥无权执行写操作」），
// 会让运维去查密钥权限，而真实原因是 key 拼错或该走普通更新入口。
func TestRequestUpdateDistinguishesKeyErrors(t *testing.T) {
	cases := []struct {
		name     string
		key      string
		wantCode string
	}{
		{"key 不在白名单", "not.a.real.key", "SETTING_KEY_NOT_ALLOWED"},
		{"key 存在但非高影响项", SettingArchiveBatchRows, "SETTING_KEY_NOT_DANGEROUS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newTestSettingsService(t)
			// 该用例只校验入口守卫，未装配 approval 亦应先被 key 校验拦下。
			_, err := svc.RequestUpdate(tc.key, "1", "验收原因", "settings-key-1", "alice", "127.0.0.1", auth.HumanPrincipal("alice"))
			if err == nil {
				t.Fatal("应拒绝，实际通过")
			}
			var ae *apperr.Error
			if !errors.As(err, &ae) {
				t.Fatalf("应为 *apperr.Error，实际 %T", err)
			}
			if ae.Code != tc.wantCode {
				t.Fatalf("错误码应为 %q，实际 %q（%s）", tc.wantCode, ae.Code, ae.Message)
			}
			if ae.Status != 400 {
				t.Fatalf("应为 400（调用方选错入口），实际 %d", ae.Status)
			}
			if ae.Code == apperr.ErrForbidden.Code {
				t.Fatal("不应复用泛化 FORBIDDEN")
			}
		})
	}
}

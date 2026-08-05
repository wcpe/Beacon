package service

import (
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

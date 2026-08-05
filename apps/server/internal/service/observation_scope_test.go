package service

import (
	"strconv"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

func TestObservationScopeResolverResolvesFrozenNamespaceSet(t *testing.T) {
	db := observationScopeTestDB(t)
	prod := observationScopeNamespace(t, db, "prod")
	staging := observationScopeNamespace(t, db, "staging")
	other := observationScopeNamespace(t, db, "other")
	env := model.Env{Code: "release", Name: "发布"}
	if err := db.Create(&env).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create([]model.EnvNamespace{{EnvID: env.ID, NamespaceID: prod.ID}, {EnvID: env.ID, NamespaceID: staging.ID}}).Error; err != nil {
		t.Fatal(err)
	}
	resolver := NewObservationScopeResolver(repository.NewEnvRepository(db), repository.NewNamespaceRepository(db))

	all, err := resolver.Resolve("", "")
	if err != nil || !all.All || !all.Contains(other.ID) {
		t.Fatalf("省略范围应保留全量语义，scope=%+v err=%v", all, err)
	}
	envScope, err := resolver.Resolve(uintText(env.ID), "")
	if err != nil || envScope.All || !envScope.Contains(prod.ID) || !envScope.Contains(staging.ID) || envScope.Contains(other.ID) || len(envScope.NamespaceCodes) != 2 {
		t.Fatalf("env 应冻结其全部 namespace，scope=%+v err=%v", envScope, err)
	}
	nsScope, err := resolver.Resolve("", uintText(prod.ID))
	if err != nil || nsScope.All || !nsScope.Contains(prod.ID) || nsScope.Contains(staging.ID) {
		t.Fatalf("namespace 应只保留单项，scope=%+v err=%v", nsScope, err)
	}
	both, err := resolver.Resolve(uintText(env.ID), uintText(staging.ID))
	if err != nil || !both.Contains(staging.ID) || both.Contains(prod.ID) {
		t.Fatalf("env + namespace 应验证归属后保留单项，scope=%+v err=%v", both, err)
	}
}

func TestObservationScopeResolverFailsClosed(t *testing.T) {
	db := observationScopeTestDB(t)
	ns := observationScopeNamespace(t, db, "prod")
	env := model.Env{Code: "empty", Name: "空环境"}
	if err := db.Create(&env).Error; err != nil {
		t.Fatal(err)
	}
	resolver := NewObservationScopeResolver(repository.NewEnvRepository(db), repository.NewNamespaceRepository(db))

	empty, err := resolver.Resolve(uintText(env.ID), "")
	if err != nil || empty.All || !empty.Empty() {
		t.Fatalf("空映射必须是合法空集，scope=%+v err=%v", empty, err)
	}
	for _, input := range [][2]string{{"0", ""}, {"-1", ""}, {"bad", ""}, {"", "0"}} {
		_, err = resolver.Resolve(input[0], input[1])
		observationScopeError(t, err, apperr.ErrInvalidObservationScope.Code)
	}
	_, err = resolver.Resolve("999", "")
	observationScopeError(t, err, apperr.ErrObservationScopeStale.Code)
	_, err = resolver.Resolve(uintText(env.ID), uintText(ns.ID))
	observationScopeError(t, err, apperr.ErrObservationScopeStale.Code)
}

func observationScopeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Env{}, &model.EnvNamespace{}, &model.Namespace{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func observationScopeNamespace(t *testing.T, db *gorm.DB, code string) model.Namespace {
	t.Helper()
	ns := model.Namespace{Code: code, Name: code}
	if err := db.Create(&ns).Error; err != nil {
		t.Fatal(err)
	}
	return ns
}

func observationScopeError(t *testing.T, err error, code string) {
	t.Helper()
	if got, ok := err.(*apperr.Error); !ok || got.Code != code {
		t.Fatalf("错误应为 %s，实际 %v", code, err)
	}
}

func uintText(value uint) string { return strconv.FormatUint(uint64(value), 10) }

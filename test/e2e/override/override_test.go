//go:build e2e

// FR-15 三方覆盖 + 受限重载命令的真机端到端测试（ADR-0011），纯 Go 原生 go test -tags=e2e。
//
// 本测试自起服务端：先构建并起控制面（默认 SQLite），再起真 Paper + BeaconAgent + BeaconE2E
// 验收插件，经 admin REST 建/发布/回滚覆盖集、经数据层挂成员（控制面尚无成员挂载 API，沿用集成
// 测试既有做法），再读验收插件写出的观测标记文件做断言。相位生命周期全在测试内（不悬挂）：
//
//	inert      控制面 UP、Paper(空白名单) UP：建集 + 挂成员 + 发布带命令 → 断言「文件被覆盖、命令一条不发」。
//	filetree   同上服务端：发布文件树文件 → 断言 agent 镜像落盘到插件数据目录（FR-14）。
//	ordering   控制面 UP、Paper(放行白名单) UP：断言「先备份原文件→落盘新内容→落盘成功后才派发命令」次序；
//	           再回滚到无命令版本 → 断言「回滚只还原事实、不重放命令」。
//	failstatic 控制面 DOWN、Paper UP：断言「控制面挂了文件不动、命令不发」（fail-static）。
//
// 铁律：本测试只读盘 + 调既有 API，绝不旁路或弱化任一 ADR-0011 安全约束来「让断言通过」。
package override_e2e

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/test/e2e/harness"
)

// 覆盖集与成员的约定常量（须与 BeaconE2E 验收插件 OverrideE2EProbe 的约定一致）。
const (
	setName    = "BeaconE2E"              // 覆盖集名（= 目标插件名）
	targetRoot = "plugins/BeaconE2E"      // 落盘根（相对 plugins）
	memberPath = "managed.yml"            // 被覆盖的成员文件（相对 targetRoot）
	reloadCmd  = "beacone2ereload"        // 受限重载命令（首 token 须在 agent 本地白名单内才派发）
	contentA   = "marker: original-A\n"   // 验收插件种下的原文件内容（将被覆盖、需备份）
	contentB   = "marker: overridden-B\n" // 覆盖集成员的新内容（覆盖后落盘）

	// 文件树（FR-14）验收：发布 path（相对 plugins 镜像根）→ agent 镜像落盘到 plugins/BeaconE2E/tree-managed.yml。
	treeFilePath = "BeaconE2E/tree-managed.yml"
	treeContent  = "tree: mirrored-C\n"
)

// 控制面地址：默认 http://localhost:8848，可经 E2E_BEACON_URL 覆盖（本地避端口争用）。
var beaconURL = harness.BeaconURL()

// 服务端编排相关常量（与 runServer gradle 任务的约定一致）。
const (
	adminUser    = "admin"
	serverID     = "e2e-bukkit-1"
	mcPort       = "25566"
	namespace    = "prod"
	bootstrap    = "beacon-bootstrap-2026"
	onlineWait   = 12 * time.Minute // 首跑含下载 Paper + 构建 jar，给足时间
	logPrefixCP  = "beacon-override"
	logPrefixMC  = "paper"
	sqliteDBName = "beacon-e2e-override.db"
)

// 一条观测记录（验收插件标记文件的一行：时间|来源|path|md5|内容）。
type obs struct {
	ts, source, path, md5, raw string
}

// pathSet 由运行根目录拼出验收插件标记文件、被覆盖文件、备份文件、文件树镜像的绝对路径。
type pathSet struct {
	obsLog, managed, backup, filetreeObs, treeMirror string
}

func runPaths(runDir string) pathSet {
	return pathSet{
		obsLog:  filepath.Join(runDir, "plugins", "BeaconE2E", "e2e-override-observations.log"),
		managed: filepath.Join(runDir, "plugins", "BeaconE2E", memberPath),
		// 备份区：plugins/BeaconAgent/override-backup/<setName>/<memberPath>（BackupManager 约定）。
		backup: filepath.Join(runDir, "plugins", "BeaconAgent", "override-backup", setName, memberPath),
		// 文件树观测标记 + 镜像落盘文件（plugins/BeaconE2E/tree-managed.yml = 发布 path 在镜像根下的落点）。
		filetreeObs: filepath.Join(runDir, "plugins", "BeaconE2E", "e2e-filetree-observations.log"),
		treeMirror:  filepath.Join(runDir, "plugins", "BeaconE2E", "tree-managed.yml"),
	}
}

// TestOverrideE2E 按相位顺序编排整套 FR-15 真机端到端：构建 → 起控制面 → 复位运行目录 →
// 起 Paper(空白名单) → inert/filetree → 杀 Paper → 起 Paper(放行白名单) → ordering →
// 杀控制面 → failstatic。defer 收口杀全部进程。
func TestOverrideE2E(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")

	dbDriver := envOr("E2E_DB_DRIVER", "sqlite")

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	runDir := filepath.Join(repoRoot, ".tmp", "e2e-run", "bukkit")
	paths := runPaths(runDir)

	// 数据库 DSN：sqlite 用 .tmp 下独立文件并起前删除（等价 Reset-Db 的干净库）；mysql 用 E2E_DB_DSN 并 TRUNCATE。
	dbDSN := resolveDBDSN(t, dbDriver, repoRoot)

	t.Log("== 构建控制面二进制 ==")
	bin, err := harness.BuildBeacon(repoRoot)
	if err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}

	t.Log("== 起控制面 + 复位运行目录 ==")
	cp, err := harness.StartControlPlane(harness.ControlPlaneConfig{
		BinPath: bin, RepoRoot: repoRoot, BaseURL: beaconURL,
		DBDriver: dbDriver, DBDSN: dbDSN,
		AdminPassword: adminPass, AuthSecret: authSecret, BootstrapToken: bootstrap,
		LogPrefix: logPrefixCP,
	})
	if err != nil {
		t.Fatalf("起控制面失败：%v", err)
	}
	cpStopped := false
	defer func() {
		if !cpStopped {
			cp.Stop()
		}
	}()
	if dbDriver == "mysql" {
		truncateOverrideTables(t, dbDriver, dbDSN)
	}
	resetRunDirMirror(runDir)

	t.Log("== 相位 inert（空白名单）+ filetree（FR-14 文件树镜像落盘）==")
	paper := startPaper(t, repoRoot, "")
	paperStopped := false
	defer func() {
		if !paperStopped {
			paper.Stop()
		}
	}()
	if err := harness.WaitInstanceOnline(beaconURL, login(t, adminPass), namespace, serverID, onlineWait); err != nil {
		t.Fatalf("inert：agent 未 online（见 .tmp/paper.out.log）：%v", err)
	}

	t.Run("inert", func(t *testing.T) { runInert(t, adminPass, dbDriver, dbDSN, paths) })
	t.Run("filetree", func(t *testing.T) { runFileTree(t, adminPass, paths) })

	paper.Stop()
	paperStopped = true

	// 强制把 inert 相位的实例标记下线，消除「陈旧 online」竞态：
	// Paper 杀掉后控制面健康 TTL 未过期仍显示 e2e-bukkit-1 online，会让下面 ordering 的
	// WaitInstanceOnline 看到残留 online 而提前返回，使 ordering 时钟在 Paper2 全新注册前空跑
	// （Paper2 经 gradle --no-daemon 重建较慢，且 CoreLib.onEnable 在主线程等首次注册才放行 BeaconE2E，
	// 故必须等到 Paper2 的全新注册，而非残留 online）。
	if tok, err := harness.Login(beaconURL, adminUser, adminPass); err != nil {
		t.Logf("ordering 前登录失败、跳过强制下线（继续）：%v", err)
	} else if err := harness.OfflineInstance(beaconURL, tok, namespace, serverID); err != nil {
		t.Logf("ordering 前强制下线失败（继续）：%v", err)
	}

	t.Log("== 相位 ordering（放行白名单：次序 + 回滚不重放）==")
	resetRunDirMirror(runDir) // 复位 managed.yml 为 A；DB 覆盖集保留（含命令）
	paper2 := startPaper(t, repoRoot, reloadCmd)
	paper2Stopped := false
	defer func() {
		if !paper2Stopped {
			paper2.Stop()
		}
	}()
	if err := harness.WaitInstanceOnline(beaconURL, login(t, adminPass), namespace, serverID, onlineWait); err != nil {
		t.Fatalf("ordering：agent 未 online：%v", err)
	}
	t.Run("ordering", func(t *testing.T) { runOrdering(t, adminPass, paths) })

	t.Log("== 相位 failstatic（杀控制面，文件不动命令不发）==")
	cp.Stop()
	cpStopped = true
	time.Sleep(3 * time.Second)
	t.Run("failstatic", func(t *testing.T) { runFailStatic(t, paths) })

	paper2.Stop()
	paper2Stopped = true
}

// startPaper 起 Paper（whitelist 非空则注入本地命令白名单，空则保持默认空 = inert）。
func startPaper(t *testing.T, repoRoot, whitelist string) *harness.GradleProc {
	t.Helper()
	props := []string{"-Pe2eMcPort=" + mcPort, harness.BeaconEndpointProp()}
	if whitelist != "" {
		props = append(props, "-Pe2eCommandWhitelist="+whitelist)
	}
	p, err := harness.StartGradleTask(repoRoot, ":agent-e2e:runServer", props, logPrefixMC)
	if err != nil {
		t.Fatalf("起 Paper 失败：%v", err)
	}
	return p
}

// ---- 相位实现（断言逻辑自原 main.go 搬入，fatalf→t.Fatalf、pass/logf→t.Log、去掉 os.Exit）----

// runInert：空白名单下，覆盖集发布后文件被覆盖、但命令一条都不派发（ADR-0011 默认 inert）。
// 本相位同时承担「建集 + 挂成员 + 发布带命令」的初始化（后续 ordering 相位复用同一覆盖集）。
func runInert(t *testing.T, password, dbDriver, dbDSN string, p pathSet) {
	token := login(t, password)

	id := ensureSet(t, token)
	ensureMember(t, dbDriver, dbDSN, id)
	publishSet(t, token, id, reloadCmd)
	t.Logf("已建/发布覆盖集 id=%d（targetRoot=%s，命令=%s）", id, targetRoot, reloadCmd)

	// 等文件被覆盖为 B（agent 应用覆盖集）。
	if !waitUntil(35*time.Second, func() bool {
		return hasChangedTo(readObs(p.obsLog), contentB)
	}) {
		t.Fatalf("inert：超时未观测到 managed.yml 被覆盖为新内容（FILE_CHANGED=B）")
	}
	// 再多等一会，确保「即便文件覆盖了，命令也始终不派发」。
	time.Sleep(8 * time.Second)

	records := readObs(p.obsLog)
	if n := count(records, "COMMAND_RECEIVED"); n != 0 {
		t.Fatalf("inert：空白名单下不应派发任何命令，却观测到 %d 条 COMMAND_RECEIVED", n)
	}
	t.Log("PASS inert：空白名单下文件已被覆盖为 B、受限重载命令一条未派发（默认 inert 成立）")
}

// runOrdering：放行白名单下，验证「备份原文件→落盘新内容→落盘成功后才派发命令」次序，再验回滚不重放命令。
func runOrdering(t *testing.T, password string, p pathSet) {
	token := login(t, password)

	// 等到「文件被覆盖为 B」且「命令已收到」都出现。
	if !waitUntil(35*time.Second, func() bool {
		r := readObs(p.obsLog)
		return hasChangedTo(r, contentB) && count(r, "COMMAND_RECEIVED") >= 1
	}) {
		t.Fatalf("ordering：超时未同时观测到 FILE_CHANGED=B 与 COMMAND_RECEIVED")
	}
	records := readObs(p.obsLog)

	// 次序断言一：命令收到时，磁盘上的内容已经是覆盖后的 B（证明文件先落盘、命令后派发）。
	cmd := firstOf(records, "COMMAND_RECEIVED")
	if cmd == nil {
		t.Fatalf("ordering：未找到 COMMAND_RECEIVED 记录")
	}
	if cmd.raw != escape(contentB) {
		t.Fatalf("ordering：命令收到时磁盘内容应已是覆盖后的 B，实际 raw=%q", cmd.raw)
	}
	// 次序断言二：确有一次「文件被覆盖为 B」的观测，证明覆盖确实落到受管文件（路径正确）。
	// 注意：FILE_CHANGED 由 1 秒轮询观测、存在采样滞后，故不以其时间戳与命令时间戳比先后；
	// 「命令收到时磁盘已是 B」（断言一）才是「先落盘、后派发」的权威证据。
	if firstChangedTo(records, contentB) == nil {
		t.Fatalf("ordering：未找到 FILE_CHANGED=B 记录（覆盖未落到受管文件）")
	}
	// 备份断言：覆盖前的原文件 A 已被备份。
	if got := readFile(p.backup); got != contentA {
		t.Fatalf("ordering：备份文件应为原文件 A，实际=%q（路径 %s）", got, p.backup)
	}
	t.Log("ordering：次序成立（先备份 A → 落盘 B → 命令在 B 落盘后派发，命令时磁盘=B）")

	// 回滚验证：回滚到 v1（无命令版本）后，agent 向目标态收敛——还原事实但绝不重放命令。
	cmdBefore := count(records, "COMMAND_RECEIVED")
	id := mustFindSet(t, token)
	rollbackSet(t, token, id, 1)
	t.Logf("ordering：已回滚覆盖集 id=%d 到 v1（无命令版本）", id)
	time.Sleep(8 * time.Second)

	after := readObs(p.obsLog)
	if n := count(after, "COMMAND_RECEIVED"); n != cmdBefore {
		t.Fatalf("ordering：回滚不应重放命令，命令数由 %d 变为 %d", cmdBefore, n)
	}
	if got := readFile(p.managed); got != contentB {
		t.Fatalf("ordering：回滚后受管文件仍应在位（B），实际=%q", got)
	}
	t.Logf("PASS ordering：次序正确，且回滚只还原事实、未重放任何重载命令（命令数仍为 %d）", cmdBefore)
}

// runFailStatic：控制面已被外部编排杀掉后，断言文件不动、命令不发（agent fail-static）。
func runFailStatic(t *testing.T, p pathSet) {
	before := readObs(p.obsLog)
	baseCmd := count(before, "COMMAND_RECEIVED")
	baseMd5 := md5File(p.managed)
	t.Logf("failstatic：控制面应已下线，基线 命令数=%d managed.md5=%s，观察 9 秒", baseCmd, baseMd5)

	time.Sleep(9 * time.Second)

	after := readObs(p.obsLog)
	if n := count(after, "COMMAND_RECEIVED"); n != baseCmd {
		t.Fatalf("failstatic：控制面挂掉后不应有新命令，命令数由 %d 变为 %d", baseCmd, n)
	}
	if m := md5File(p.managed); m != baseMd5 {
		t.Fatalf("failstatic：控制面挂掉后受管文件不应变动，md5 由 %s 变为 %s", baseMd5, m)
	}
	t.Log("PASS failstatic：控制面下线期间受管文件未变、命令未派发（fail-static 成立）")
}

// runFileTree：发布一个文件树文件 → 断言 agent 镜像落盘到插件真实数据目录、验收插件读到镜像内容（FR-14）。
func runFileTree(t *testing.T, password string, p pathSet) {
	token := login(t, password)

	publishTreeFile(t, token)
	t.Logf("已发布文件树文件 path=%s（应镜像落盘到插件数据目录）", treeFilePath)

	// 等验收插件观测到镜像文件内容为发布内容（agent 已镜像落盘到 plugins/BeaconE2E/tree-managed.yml）。
	if !waitUntil(35*time.Second, func() bool {
		return hasMirrored(readObs(p.filetreeObs), treeContent)
	}) {
		t.Fatalf("filetree：超时未观测到文件树文件被镜像落盘到插件数据目录（FILE_TREE_MIRRORED=C）")
	}
	// 双保险：直接核对镜像文件确实落在插件真实数据目录、内容正确。
	if got := readFile(p.treeMirror); got != treeContent {
		t.Fatalf("filetree：镜像文件应落在插件数据目录且内容为发布内容，实际=%q（路径 %s）", got, p.treeMirror)
	}
	t.Log("PASS filetree：文件树文件已镜像落盘到插件真实数据目录、验收插件读到镜像内容（FR-14 成立）")
}

// ---- 覆盖集 REST ----

// ensureSet 查到则复用、查不到则新建（空命令、global 层）覆盖集，返回其 id。
func ensureSet(t *testing.T, token string) uint {
	if id, ok := findSet(t, token); ok {
		return id
	}
	body := map[string]any{
		"namespace": "prod", "group": model.GlobalGroupCode, "name": setName,
		"scopeLevel": model.ScopeGlobal, "scopeTarget": "",
		"targetRoot": targetRoot, "reloadCommand": "", "comment": "e2e 初始化（空命令）",
	}
	var resp struct {
		ID uint `json:"id"`
	}
	doAdmin(t, http.MethodPost, "/admin/v1/override-sets", token, body, http.StatusCreated, &resp)
	return resp.ID
}

// findSet 按名查覆盖集 id。
func findSet(t *testing.T, token string) (uint, bool) {
	var resp struct {
		Items []struct {
			ID   uint   `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	doAdmin(t, http.MethodGet, "/admin/v1/override-sets?namespace=prod", token, nil, http.StatusOK, &resp)
	for _, it := range resp.Items {
		if it.Name == setName {
			return it.ID, true
		}
	}
	return 0, false
}

func mustFindSet(t *testing.T, token string) uint {
	if id, ok := findSet(t, token); ok {
		return id
	}
	t.Fatalf("未找到覆盖集 %s", setName)
	return 0
}

// publishSet 发布新版本：设定目标根 + 受限重载命令。
func publishSet(t *testing.T, token string, id uint, cmd string) {
	body := map[string]any{"targetRoot": targetRoot, "reloadCommand": cmd, "comment": "e2e 发布命令"}
	doAdmin(t, http.MethodPut, fmt.Sprintf("/admin/v1/override-sets/%d", id), token, body, http.StatusOK, nil)
}

// rollbackSet 回滚到目标版本（新版本 = 当前 +1，只还原事实）。
func rollbackSet(t *testing.T, token string, id uint, toVersion int) {
	body := map[string]any{"toVersion": toVersion, "comment": "e2e 回滚验证"}
	doAdmin(t, http.MethodPost, fmt.Sprintf("/admin/v1/override-sets/%d/rollback", id), token, body, http.StatusOK, nil)
}

// publishTreeFile 经 admin REST 建一个文件树文件（global 层），触发 agent 文件树镜像落盘（FR-14）。
func publishTreeFile(t *testing.T, token string) {
	body := map[string]any{
		"namespace": "prod", "group": model.GlobalGroupCode, "path": treeFilePath,
		"scopeLevel": model.ScopeGlobal, "scopeTarget": "",
		"content": treeContent, "comment": "e2e 文件树镜像验收",
	}
	doAdmin(t, http.MethodPost, "/admin/v1/files", token, body, http.StatusCreated, nil)
}

// ensureMember 经数据层把成员文件 managed.yml=B 挂到覆盖集（控制面无成员挂载 API，沿用集成测试做法）。
func ensureMember(t *testing.T, dbDriver, dbDSN string, setID uint) {
	db := openE2EDB(t, dbDriver, dbDSN)
	var existing model.FileObject
	e := db.Where("override_set_id = ? AND path = ?", setID, memberPath).First(&existing).Error
	if e == nil {
		return // 已挂载
	}
	if !errors.Is(e, gorm.ErrRecordNotFound) {
		t.Fatalf("查询成员文件失败：%v", e)
	}
	sum := md5.Sum([]byte(contentB))
	obj := &model.FileObject{
		NamespaceCode: "prod", GroupCode: model.GlobalGroupCode, Path: memberPath,
		ScopeLevel: model.ScopeGlobal, Content: contentB, ContentMD5: hex.EncodeToString(sum[:]),
		Version: 1, Enabled: true, OverrideSetID: setID,
	}
	if err := repository.NewFileObjectRepository(db).Create(obj); err != nil {
		t.Fatalf("挂载成员文件失败：%v", err)
	}
}

// ---- 数据库分支（sqlite 与控制面共享同一文件，mysql 用 E2E_DB_DSN）----

// resolveDBDSN 按驱动决定控制面 DSN：sqlite 用 .tmp 下独立文件并起前删除（干净库）；mysql 取 E2E_DB_DSN。
func resolveDBDSN(t *testing.T, dbDriver, repoRoot string) string {
	switch dbDriver {
	case "sqlite":
		dsn := filepath.Join(repoRoot, ".tmp", sqliteDBName)
		if err := os.MkdirAll(filepath.Dir(dsn), 0o755); err != nil {
			t.Fatalf("创建 .tmp 目录失败：%v", err)
		}
		// 起前删除旧库，等价 Reset-Db 的 TRUNCATE，从干净 v1 开始。
		_ = os.Remove(dsn)
		return dsn
	case "mysql":
		return requireEnv(t, "E2E_DB_DSN")
	default:
		t.Fatalf("不支持的 E2E_DB_DRIVER %q（应为 sqlite|mysql）", dbDriver)
		return ""
	}
}

// openE2EDB 按驱动打开与控制面同一份数据（sqlite 加 _busy_timeout 缓解共享文件写锁竞争）。
func openE2EDB(t *testing.T, dbDriver, dbDSN string) *gorm.DB {
	var dialector gorm.Dialector
	switch dbDriver {
	case "sqlite":
		// 与控制面共享同一文件，加忙等超时降低写锁冲突。
		dsn := dbDSN
		if !strings.Contains(dsn, "?") {
			dsn += "?_busy_timeout=5000"
		}
		dialector = sqlite.Open(dsn)
	case "mysql":
		dialector = mysql.Open(dbDSN)
	default:
		t.Fatalf("不支持的 E2E_DB_DRIVER %q（应为 sqlite|mysql）", dbDriver)
	}
	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		t.Fatalf("连接数据库失败：%v", err)
	}
	return db
}

// truncateOverrideTables 仅 mysql 路径用：清覆盖/文件三表，整跑从干净 v1 开始（等价 Reset-Db）。
func truncateOverrideTables(t *testing.T, dbDriver, dbDSN string) {
	db := openE2EDB(t, dbDriver, dbDSN)
	for _, tbl := range []string{"file_object", "file_override_set", "file_override_set_revision"} {
		if err := db.Exec("TRUNCATE TABLE " + tbl).Error; err != nil {
			t.Fatalf("清空表 %s 失败：%v", tbl, err)
		}
	}
}

// ---- 鉴权 ----

// login 用管理员口令换登录令牌（FR-11）；编排阶段（等 online）与相位内取令牌共用。
func login(t *testing.T, pass string) string {
	token, err := harness.Login(beaconURL, adminUser, pass)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return token
}

// ---- HTTP 工具 ----

// doAdmin 发一个带 Bearer 的 admin 请求，校验期望状态码，并（若 out 非 nil）解析响应体。失败即 fatal。
func doAdmin(t *testing.T, method, path, token string, body any, wantStatus int, out any) {
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, beaconURL+path, reader)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求 %s %s 失败：%v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s 期望 HTTP %d，得 %d：%s", method, path, wantStatus, resp.StatusCode, string(raw))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("解析 %s 响应失败：%v（%s）", path, err, string(raw))
		}
	}
}

// ---- 观测标记文件解析 ----

// readObs 读取标记文件并解析为观测列表；文件不存在视为空（尚未产生观测）。
func readObs(path string) []obs {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []obs
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// 时间|来源|path|md5|内容（内容里换行已转义为 \n，但可能含 |，故 SplitN 限 5 段）。
		parts := strings.SplitN(line, "|", 5)
		if len(parts) < 5 {
			continue
		}
		out = append(out, obs{ts: parts[0], source: parts[1], path: parts[2], md5: parts[3], raw: parts[4]})
	}
	return out
}

func count(rs []obs, source string) int {
	n := 0
	for _, r := range rs {
		if r.source == source {
			n++
		}
	}
	return n
}

func firstOf(rs []obs, source string) *obs {
	for i := range rs {
		if rs[i].source == source {
			return &rs[i]
		}
	}
	return nil
}

// hasChangedTo 判断是否出现一条 FILE_CHANGED，其内容等于 want（按标记文件转义后比对）。
func hasChangedTo(rs []obs, want string) bool {
	return firstChangedTo(rs, want) != nil
}

func firstChangedTo(rs []obs, want string) *obs {
	esc := escape(want)
	for i := range rs {
		if rs[i].source == "FILE_CHANGED" && rs[i].raw == esc {
			return &rs[i]
		}
	}
	return nil
}

// hasMirrored 判断是否出现一条 FILE_TREE_MIRRORED，其内容等于 want（按标记文件转义后比对）。
func hasMirrored(rs []obs, want string) bool {
	esc := escape(want)
	for i := range rs {
		if rs[i].source == "FILE_TREE_MIRRORED" && rs[i].raw == esc {
			return true
		}
	}
	return false
}

// escape 与验收插件 OverrideE2EProbe.append 的转义保持一致（\\→\\\\，\n→\\n，去掉 \r）。
func escape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// ---- 运行目录复位 ----

// resetRunDirMirror 复位运行目录的镜像/覆盖状态：删受管文件（验收插件 ENABLE 时重种原文件 A）、覆盖与文件树观测日志、
// 陈旧备份、误落 plugins/plugins、文件树镜像文件与 agent 已落盘清单，确保每轮观测的是 agent 本轮新落的内容。
func resetRunDirMirror(runDir string) {
	_ = os.RemoveAll(filepath.Join(runDir, "plugins", "plugins"))
	_ = os.Remove(filepath.Join(runDir, "plugins", "BeaconE2E", "managed.yml"))
	_ = os.Remove(filepath.Join(runDir, "plugins", "BeaconE2E", "e2e-override-observations.log"))
	_ = os.RemoveAll(filepath.Join(runDir, "plugins", "BeaconAgent", "override-backup"))
	_ = os.Remove(filepath.Join(runDir, "plugins", "BeaconE2E", "tree-managed.yml"))
	_ = os.Remove(filepath.Join(runDir, "plugins", "BeaconE2E", "e2e-filetree-observations.log"))
	_ = os.Remove(filepath.Join(runDir, "plugins", "BeaconAgent", "file-tree.applied.json"))
}

// ---- 小工具 ----

func readFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

func md5File(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "<缺失>"
	}
	sum := md5.Sum(raw)
	return hex.EncodeToString(sum[:])
}

func waitUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(1 * time.Second)
	}
	return cond()
}

// requireEnv 取必填 env，缺失即 t.Skip（让普通 go test ./... 不因缺密钥失败）。
func requireEnv(t *testing.T, key string) string {
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("跳过：缺少必需环境变量 %s（仅在显式 -tags=e2e 且注入密钥时运行）", key)
	}
	return v
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

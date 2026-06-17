//go:build e2e

// FR-15 三方覆盖 + 受限重载命令的真机端到端驱动（ADR-0011）。
//
// 它不直接起服务端：由外部编排（OPERATIONS §7 的 runbook / 脚本）先把控制面、真 Paper + BeaconAgent
// + BeaconE2E 验收插件拉起，本驱动只负责经 admin REST 建/发布/回滚覆盖集、经数据层挂成员（控制面尚无
// 成员挂载 API，沿用集成测试既有做法），再读验收插件写出的观测标记文件做断言。
//
// 分三个相位（由编排按服务端状态分别调用）：
//
//	-phase=inert      控制面 UP、Paper(空白名单) UP：建集 + 挂成员 + 发布带命令 → 断言「文件被覆盖、命令一条不发」（默认 inert）。
//	-phase=ordering   控制面 UP、Paper(放行白名单) UP：断言「先备份原文件→落盘新内容→落盘成功后才派发命令」次序；
//	                  再回滚到无命令版本 → 断言「回滚只还原事实、不重放命令」。
//	-phase=failstatic 控制面 DOWN、Paper UP：断言「控制面挂了文件不动、命令不发」（fail-static）。
//
// 铁律：本驱动只读盘 + 调既有 API，绝不旁路或弱化任一 ADR-0011 安全约束来「让断言通过」。
package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"beacon/internal/model"
	"beacon/internal/repository"
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

// 一条观测记录（验收插件标记文件的一行：时间|来源|path|md5|内容）。
type obs struct {
	ts, source, path, md5, raw string
}

func main() {
	phase := flag.String("phase", "", "相位：inert | ordering | failstatic")
	flag.Parse()

	baseURL := env("E2E_BEACON_URL", "http://localhost:8848")
	adminUser := env("E2E_ADMIN_USER", "admin")
	adminPass := mustEnv("E2E_ADMIN_PASS")
	serverID := env("E2E_SERVER_ID", "e2e-bukkit-1")
	runDir := mustEnv("E2E_RUN_DIR")

	paths := runPaths(runDir)

	switch *phase {
	case "inert":
		runInert(baseURL, adminUser, adminPass, serverID, paths)
	case "ordering":
		runOrdering(baseURL, adminUser, adminPass, serverID, paths)
	case "failstatic":
		runFailStatic(paths)
	case "filetree":
		runFileTree(baseURL, adminUser, adminPass, serverID, paths)
	default:
		fatalf("未知相位 %q（应为 inert|ordering|failstatic|filetree）", *phase)
	}
}

// runPaths 由运行根目录拼出验收插件标记文件、被覆盖文件、备份文件、文件树镜像的绝对路径。
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

// ---- 相位实现 ----

// runInert：空白名单下，覆盖集发布后文件被覆盖、但命令一条都不派发（ADR-0011 默认 inert）。
// 本相位同时承担「建集 + 挂成员 + 发布带命令」的初始化（后续 ordering 相位复用同一覆盖集）。
func runInert(baseURL, user, password, serverID string, p pathSet) {
	token := login(baseURL, user, password)
	waitInstanceOnline(baseURL, token, serverID)

	id := ensureSet(baseURL, token)
	ensureMember(id)
	publishSet(baseURL, token, id, reloadCmd)
	logf("已建/发布覆盖集 id=%d（targetRoot=%s，命令=%s）", id, targetRoot, reloadCmd)

	// 等文件被覆盖为 B（agent 应用覆盖集）。
	if !waitUntil(35*time.Second, func() bool {
		return hasChangedTo(readObs(p.obsLog), contentB)
	}) {
		fatalf("inert：超时未观测到 managed.yml 被覆盖为新内容（FILE_CHANGED=B）")
	}
	// 再多等一会，确保「即便文件覆盖了，命令也始终不派发」。
	time.Sleep(8 * time.Second)

	records := readObs(p.obsLog)
	if n := count(records, "COMMAND_RECEIVED"); n != 0 {
		fatalf("inert：空白名单下不应派发任何命令，却观测到 %d 条 COMMAND_RECEIVED", n)
	}
	pass("inert：空白名单下文件已被覆盖为 B、受限重载命令一条未派发（默认 inert 成立）")
}

// runOrdering：放行白名单下，验证「备份原文件→落盘新内容→落盘成功后才派发命令」次序，再验回滚不重放命令。
func runOrdering(baseURL, user, password, serverID string, p pathSet) {
	token := login(baseURL, user, password)
	waitInstanceOnline(baseURL, token, serverID)

	// 等到「文件被覆盖为 B」且「命令已收到」都出现。
	if !waitUntil(35*time.Second, func() bool {
		r := readObs(p.obsLog)
		return hasChangedTo(r, contentB) && count(r, "COMMAND_RECEIVED") >= 1
	}) {
		fatalf("ordering：超时未同时观测到 FILE_CHANGED=B 与 COMMAND_RECEIVED")
	}
	records := readObs(p.obsLog)

	// 次序断言一：命令收到时，磁盘上的内容已经是覆盖后的 B（证明文件先落盘、命令后派发）。
	cmd := firstOf(records, "COMMAND_RECEIVED")
	if cmd == nil {
		fatalf("ordering：未找到 COMMAND_RECEIVED 记录")
	}
	if cmd.raw != escape(contentB) {
		fatalf("ordering：命令收到时磁盘内容应已是覆盖后的 B，实际 raw=%q", cmd.raw)
	}
	// 次序断言二：确有一次「文件被覆盖为 B」的观测，证明覆盖确实落到受管文件（路径正确）。
	// 注意：FILE_CHANGED 由 1 秒轮询观测、存在采样滞后，故不以其时间戳与命令时间戳比先后；
	// 「命令收到时磁盘已是 B」（断言一）才是「先落盘、后派发」的权威证据。
	if firstChangedTo(records, contentB) == nil {
		fatalf("ordering：未找到 FILE_CHANGED=B 记录（覆盖未落到受管文件）")
	}
	// 备份断言：覆盖前的原文件 A 已被备份。
	if got := readFile(p.backup); got != contentA {
		fatalf("ordering：备份文件应为原文件 A，实际=%q（路径 %s）", got, p.backup)
	}
	logf("ordering：次序成立（先备份 A → 落盘 B → 命令在 B 落盘后派发，命令时磁盘=B）")

	// 回滚验证：回滚到 v1（无命令版本）后，agent 向目标态收敛——还原事实但绝不重放命令。
	cmdBefore := count(records, "COMMAND_RECEIVED")
	id := mustFindSet(baseURL, token)
	rollbackSet(baseURL, token, id, 1)
	logf("ordering：已回滚覆盖集 id=%d 到 v1（无命令版本）", id)
	time.Sleep(8 * time.Second)

	after := readObs(p.obsLog)
	if n := count(after, "COMMAND_RECEIVED"); n != cmdBefore {
		fatalf("ordering：回滚不应重放命令，命令数由 %d 变为 %d", cmdBefore, n)
	}
	if got := readFile(p.managed); got != contentB {
		fatalf("ordering：回滚后受管文件仍应在位（B），实际=%q", got)
	}
	pass("ordering：次序正确，且回滚只还原事实、未重放任何重载命令（命令数仍为 " + itoa(cmdBefore) + "）")
}

// runFailStatic：控制面已被外部编排杀掉后，断言文件不动、命令不发（agent fail-static）。
func runFailStatic(p pathSet) {
	before := readObs(p.obsLog)
	baseCmd := count(before, "COMMAND_RECEIVED")
	baseMd5 := md5File(p.managed)
	logf("failstatic：控制面应已下线，基线 命令数=%d managed.md5=%s，观察 9 秒", baseCmd, baseMd5)

	time.Sleep(9 * time.Second)

	after := readObs(p.obsLog)
	if n := count(after, "COMMAND_RECEIVED"); n != baseCmd {
		fatalf("failstatic：控制面挂掉后不应有新命令，命令数由 %d 变为 %d", baseCmd, n)
	}
	if m := md5File(p.managed); m != baseMd5 {
		fatalf("failstatic：控制面挂掉后受管文件不应变动，md5 由 %s 变为 %s", baseMd5, m)
	}
	pass("failstatic：控制面下线期间受管文件未变、命令未派发（fail-static 成立）")
}

// runFileTree：发布一个文件树文件 → 断言 agent 镜像落盘到插件真实数据目录、验收插件读到镜像内容（FR-14）。
func runFileTree(baseURL, user, password, serverID string, p pathSet) {
	token := login(baseURL, user, password)
	waitInstanceOnline(baseURL, token, serverID)

	publishTreeFile(baseURL, token)
	logf("已发布文件树文件 path=%s（应镜像落盘到插件数据目录）", treeFilePath)

	// 等验收插件观测到镜像文件内容为发布内容（agent 已镜像落盘到 plugins/BeaconE2E/tree-managed.yml）。
	if !waitUntil(35*time.Second, func() bool {
		return hasMirrored(readObs(p.filetreeObs), treeContent)
	}) {
		fatalf("filetree：超时未观测到文件树文件被镜像落盘到插件数据目录（FILE_TREE_MIRRORED=C）")
	}
	// 双保险：直接核对镜像文件确实落在插件真实数据目录、内容正确。
	if got := readFile(p.treeMirror); got != treeContent {
		fatalf("filetree：镜像文件应落在插件数据目录且内容为发布内容，实际=%q（路径 %s）", got, p.treeMirror)
	}
	pass("filetree：文件树文件已镜像落盘到插件真实数据目录、验收插件读到镜像内容（FR-14 成立）")
}

// ---- 覆盖集 REST ----

// ensureSet 查到则复用、查不到则新建（空命令、global 层）覆盖集，返回其 id。
func ensureSet(baseURL, token string) uint {
	if id, ok := findSet(baseURL, token); ok {
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
	doAdmin(baseURL, http.MethodPost, "/admin/v1/override-sets", token, body, http.StatusCreated, &resp)
	return resp.ID
}

// findSet 按名查覆盖集 id。
func findSet(baseURL, token string) (uint, bool) {
	var resp struct {
		Items []struct {
			ID   uint   `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	doAdmin(baseURL, http.MethodGet, "/admin/v1/override-sets?namespace=prod", token, nil, http.StatusOK, &resp)
	for _, it := range resp.Items {
		if it.Name == setName {
			return it.ID, true
		}
	}
	return 0, false
}

func mustFindSet(baseURL, token string) uint {
	if id, ok := findSet(baseURL, token); ok {
		return id
	}
	fatalf("未找到覆盖集 %s", setName)
	return 0
}

// publishSet 发布新版本：设定目标根 + 受限重载命令。
func publishSet(baseURL, token string, id uint, cmd string) {
	body := map[string]any{"targetRoot": targetRoot, "reloadCommand": cmd, "comment": "e2e 发布命令"}
	doAdmin(baseURL, http.MethodPut, fmt.Sprintf("/admin/v1/override-sets/%d", id), token, body, http.StatusOK, nil)
}

// rollbackSet 回滚到目标版本（新版本 = 当前 +1，只还原事实）。
func rollbackSet(baseURL, token string, id uint, toVersion int) {
	body := map[string]any{"toVersion": toVersion, "comment": "e2e 回滚验证"}
	doAdmin(baseURL, http.MethodPost, fmt.Sprintf("/admin/v1/override-sets/%d/rollback", id), token, body, http.StatusOK, nil)
}

// publishTreeFile 经 admin REST 建一个文件树文件（global 层），触发 agent 文件树镜像落盘（FR-14）。
func publishTreeFile(baseURL, token string) {
	body := map[string]any{
		"namespace": "prod", "group": model.GlobalGroupCode, "path": treeFilePath,
		"scopeLevel": model.ScopeGlobal, "scopeTarget": "",
		"content": treeContent, "comment": "e2e 文件树镜像验收",
	}
	doAdmin(baseURL, http.MethodPost, "/admin/v1/files", token, body, http.StatusCreated, nil)
}

// ensureMember 经数据层把成员文件 managed.yml=B 挂到覆盖集（控制面无成员挂载 API，沿用集成测试做法）。
func ensureMember(setID uint) {
	dsn := mustEnv("E2E_DB_DSN")
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		fatalf("连接数据库失败：%v", err)
	}
	var existing model.FileObject
	e := db.Where("override_set_id = ? AND path = ?", setID, memberPath).First(&existing).Error
	if e == nil {
		return // 已挂载
	}
	if !errors.Is(e, gorm.ErrRecordNotFound) {
		fatalf("查询成员文件失败：%v", e)
	}
	sum := md5.Sum([]byte(contentB))
	obj := &model.FileObject{
		NamespaceCode: "prod", GroupCode: model.GlobalGroupCode, Path: memberPath,
		ScopeLevel: model.ScopeGlobal, Content: contentB, ContentMD5: hex.EncodeToString(sum[:]),
		Version: 1, Enabled: true, OverrideSetID: setID,
	}
	if err := repository.NewFileObjectRepository(db).Create(obj); err != nil {
		fatalf("挂载成员文件失败：%v", err)
	}
}

// ---- 鉴权与实例 ----

// login 用管理员口令换登录令牌（FR-11）。
func login(baseURL, user, pass string) string {
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	resp, err := http.Post(baseURL+"/admin/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		fatalf("登录请求失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		fatalf("登录失败：HTTP %d %s", resp.StatusCode, string(raw))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Token == "" {
		fatalf("登录响应无 token：%v", err)
	}
	return out.Token
}

// waitInstanceOnline 轮询直到目标 serverId 在控制面 online（agent 已注册接入）。
func waitInstanceOnline(baseURL, token, serverID string) {
	ok := waitUntil(40*time.Second, func() bool {
		var resp struct {
			Items []struct {
				ServerID string `json:"serverId"`
				Status   string `json:"status"`
			} `json:"items"`
		}
		if !tryAdmin(baseURL, http.MethodGet, "/admin/v1/instances?namespace=prod", token, &resp) {
			return false
		}
		for _, it := range resp.Items {
			if it.ServerID == serverID && it.Status == "online" {
				return true
			}
		}
		return false
	})
	if !ok {
		fatalf("等待 agent 实例 %s online 超时", serverID)
	}
	logf("agent 实例 %s 已 online", serverID)
}

// ---- HTTP 工具 ----

// doAdmin 发一个带 Bearer 的 admin 请求，校验期望状态码，并（若 out 非 nil）解析响应体。失败即 fatal。
func doAdmin(baseURL, method, path, token string, body any, wantStatus int, out any) {
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, baseURL+path, reader)
	if err != nil {
		fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatalf("请求 %s %s 失败：%v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		fatalf("%s %s 期望 HTTP %d，得 %d：%s", method, path, wantStatus, resp.StatusCode, string(raw))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			fatalf("解析 %s 响应失败：%v（%s）", path, err, string(raw))
		}
	}
}

// tryAdmin 发一个 admin GET，仅在 200 且能解析时返回 true（用于轮询，不 fatal）。
func tryAdmin(baseURL, method, path, token string, out any) bool {
	req, err := http.NewRequest(method, baseURL+path, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	raw, _ := io.ReadAll(resp.Body)
	return json.Unmarshal(raw, out) == nil
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

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fatalf("缺少必需环境变量 %s", key)
	}
	return v
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "[e2e] "+format+"\n", a...)
}

func pass(msg string) {
	fmt.Fprintf(os.Stdout, "PASS %s\n", msg)
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stdout, "FAIL "+format+"\n", a...)
	os.Exit(1)
}

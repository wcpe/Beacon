#requires -Version 5
<#
.SYNOPSIS
  FR-15 三方覆盖 + 受限重载命令的真机端到端编排（ADR-0011）。

.DESCRIPTION
  前置：MySQL（mysql:8.0 一次性容器，默认 host 端口 33306）已就绪；本机有 Go / JDK21 / Docker。
  本脚本负责：起控制面（beacon 二进制）→ 起真 Paper + BeaconAgent + BeaconE2E 验收插件 →
  调 Go 驱动（test/e2e/override）按三相位断言，逐相位收口、Paper 生命周期全在脚本内（不悬挂）：

    相位1 inert      空白名单：覆盖集发布后文件被覆盖、但受限重载命令一条不发（默认 inert，ADR-0011 决策3）。
    相位2 ordering   放行白名单：验「备份原文件→落盘新内容→落盘成功后才派发命令」次序，再回滚到无命令版本验「只还原不重放」。
    相位3 failstatic 杀控制面：受管文件不动、命令不发（fail-static）。

  敏感项（管理员口令 / 令牌签名密钥）经参数或环境变量注入，绝不写死入库。

.EXAMPLE
  $env:E2E_ADMIN_PASS='xxx'; $env:E2E_AUTH_SECRET='yyy'
  pwsh ./test/e2e/override/run-override-e2e.ps1
#>
[CmdletBinding()]
param(
    # 控制面二进制（go build -o .tmp/beacon-e2e.exe ./cmd/beacon 产出）
    [string]$BeaconBin = "$PSScriptRoot\..\..\..\.tmp\beacon-e2e.exe",
    # 仓库根
    [string]$RepoRoot = "$PSScriptRoot\..\..\..",
    # Paper 运行目录（runServer 落点）
    [string]$RunDir = "$PSScriptRoot\..\..\..\.tmp\e2e-run\bukkit",
    # 控制面地址
    [string]$BeaconUrl = "http://localhost:8848",
    # Paper 监听端口（避让本机 25565）
    [int]$McPort = 25566,
    [string]$AdminUser = "admin",
    # 管理员口令（必填，走 env E2E_ADMIN_PASS）
    [string]$AdminPass = $env:E2E_ADMIN_PASS,
    # 令牌签名密钥（必填，走 env E2E_AUTH_SECRET）
    [string]$AuthSecret = $env:E2E_AUTH_SECRET,
    # agent 共享令牌（X-Beacon-Token）
    [string]$BootstrapToken = "beacon-bootstrap-2026",
    # MySQL 容器名与连接分片（拼 DSN，不在命令行出现完整含 ?charset 的串）
    [string]$MysqlContainer = "beacon-e2e-mysql",
    [string]$MysqlHost = "127.0.0.1",
    [int]$MysqlPort = 33306,
    [string]$MysqlDb = "beacon",
    [string]$MysqlUser = "root",
    [string]$MysqlPass = "beacon"
)

$ErrorActionPreference = "Stop"
# 清掉环境里误设的 docker tcp 端点，回落默认 context（命名管道）
$env:DOCKER_HOST = $null

if (-not $AdminPass) { throw "缺少管理员口令：经 -AdminPass 或 env E2E_ADMIN_PASS 注入" }
if (-not $AuthSecret) { throw "缺少令牌签名密钥：经 -AuthSecret 或 env E2E_AUTH_SECRET 注入" }

# DSN 分片拼装（避免把含 ?charset 的完整串写进调用命令行，规避误判）
$dsn = "{0}:{1}@tcp({2}:{3})/{4}?charset=utf8mb4&parseTime=true&loc=UTC" -f $MysqlUser, $MysqlPass, $MysqlHost, $MysqlPort, $MysqlDb

$script:beaconProc = $null
$script:paperProc = $null

# 杀占用指定端口的进程（按端口精准定位，绝不误伤其它 java 服）
function Stop-Port([int]$port) {
    Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue |
        ForEach-Object { Stop-Process -Id $_.OwningProcess -Force -ErrorAction SilentlyContinue }
}

# 取管理员登录令牌（FR-11）
function Get-Token {
    $body = @{ username = $AdminUser; password = $AdminPass } | ConvertTo-Json -Compress
    (Invoke-RestMethod "$BeaconUrl/admin/v1/auth/login" -Method POST -Body $body -ContentType 'application/json').token
}

# 起控制面并等可达（清掉旧 8848，全新内存注册表）
function Start-BeaconCp {
    Stop-Port 8848
    Start-Sleep -Seconds 2
    $env:BEACON_DB_DSN = $dsn
    $env:BEACON_ADMIN_PASSWORD = $AdminPass
    $env:BEACON_AUTH_SECRET = $AuthSecret
    $env:BEACON_BOOTSTRAP_TOKEN = $BootstrapToken
    $env:BEACON_LOG_LEVEL = "INFO"
    $script:beaconProc = Start-Process $BeaconBin -PassThru `
        -RedirectStandardOutput "$RepoRoot\.tmp\beacon.out.log" -RedirectStandardError "$RepoRoot\.tmp\beacon.err.log"
    for ($i = 0; $i -lt 30; $i++) {
        try { Invoke-WebRequest "$BeaconUrl/admin/v1/auth/login" -Method POST -Body '{}' -ContentType 'application/json' -UseBasicParsing | Out-Null; return }
        catch { if ($_.Exception.Response) { return } }
        Start-Sleep -Seconds 1
    }
    throw "控制面未在预期时间内就绪"
}

# 杀控制面（fail-static 相位用）
function Stop-BeaconCp {
    Stop-Port 8848
    if ($script:beaconProc) { Stop-Process -Id $script:beaconProc.Id -Force -ErrorAction SilentlyContinue }
}

# 清 DB 覆盖/文件表（整跑从干净 v1 开始）
function Reset-Db {
    docker exec $MysqlContainer mysql "-u$MysqlUser" "-p$MysqlPass" -e `
        "TRUNCATE $MysqlDb.file_object; TRUNCATE $MysqlDb.file_override_set; TRUNCATE $MysqlDb.file_override_set_revision;" 2>$null
}

# 复位运行目录的镜像/覆盖状态：删受管文件（验收插件 ENABLE 时重种原文件 A）、覆盖与文件树观测日志、
# 陈旧备份、误落 plugins\plugins、文件树镜像文件与 agent 已落盘清单，确保每轮观测的是 agent 本轮新落的内容。
function Reset-RunDirMirror {
    Remove-Item -Recurse -Force (Join-Path $RunDir "plugins\plugins") -ErrorAction SilentlyContinue
    Remove-Item -Force (Join-Path $RunDir "plugins\BeaconE2E\managed.yml") -ErrorAction SilentlyContinue
    Remove-Item -Force (Join-Path $RunDir "plugins\BeaconE2E\e2e-override-observations.log") -ErrorAction SilentlyContinue
    Remove-Item -Recurse -Force (Join-Path $RunDir "plugins\BeaconAgent\override-backup") -ErrorAction SilentlyContinue
    Remove-Item -Force (Join-Path $RunDir "plugins\BeaconE2E\tree-managed.yml") -ErrorAction SilentlyContinue
    Remove-Item -Force (Join-Path $RunDir "plugins\BeaconE2E\e2e-filetree-observations.log") -ErrorAction SilentlyContinue
    Remove-Item -Force (Join-Path $RunDir "plugins\BeaconAgent\file-tree.applied.json") -ErrorAction SilentlyContinue
}

# 起 Paper（whitelist 非空则注入本地命令白名单，空则保持默认空 = inert）
function Start-Paper([string]$whitelist) {
    Stop-Port $McPort
    Start-Sleep -Seconds 2
    $gargs = @(":agent-e2e:runServer", "-Pe2eMcPort=$McPort", "--console=plain")
    if ($whitelist) { $gargs += "-Pe2eCommandWhitelist=$whitelist" }
    $script:paperProc = Start-Process "$RepoRoot\agent\gradlew.bat" -ArgumentList $gargs -WorkingDirectory "$RepoRoot\agent" -PassThru `
        -RedirectStandardOutput "$RepoRoot\.tmp\paper.out.log" -RedirectStandardError "$RepoRoot\.tmp\paper.err.log"
}

# 等 agent 在控制面 online
function Wait-Online {
    $tok = Get-Token
    for ($i = 0; $i -lt 35; $i++) {
        Start-Sleep -Seconds 4
        try {
            $r = Invoke-RestMethod "$BeaconUrl/admin/v1/instances?namespace=prod" -Headers @{ Authorization = "Bearer $tok" }
            if ($r.items | Where-Object { $_.serverId -eq 'e2e-bukkit-1' -and $_.status -eq 'online' }) { return $true }
        } catch {}
    }
    return $false
}

# 杀 Paper（相位收口，进程不悬挂）
function Stop-Paper {
    Stop-Port $McPort
    if ($script:paperProc) { Stop-Process -Id $script:paperProc.Id -Force -ErrorAction SilentlyContinue }
    $script:paperProc = $null
}

# 跑某相位驱动，返回退出码（0=PASS）
function Invoke-Driver([string]$phase) {
    $env:E2E_ADMIN_PASS = $AdminPass
    $env:E2E_DB_DSN = $dsn
    $env:E2E_RUN_DIR = $RunDir
    $env:E2E_BEACON_URL = $BeaconUrl
    $env:E2E_SERVER_ID = "e2e-bukkit-1"
    Push-Location $RepoRoot
    try {
        # 驱动输出经管道写到 host 显示，避免混进函数返回值；函数只返回退出码（0=PASS）。
        & go run -tags=e2e ./test/e2e/override "-phase=$phase" 2>&1 | ForEach-Object { Write-Host $_ }
        return $LASTEXITCODE
    }
    finally { Pop-Location }
}

$results = [ordered]@{}
try {
    Write-Host "== 起控制面 + 清 DB + 复位运行目录 =="
    Start-BeaconCp
    Reset-Db
    Reset-RunDirMirror

    Write-Host "`n== 相位1 inert（空白名单，默认 inert）+ filetree（FR-14 文件树镜像落盘）=="
    Start-Paper $null
    if (-not (Wait-Online)) { Stop-Paper; throw "inert：agent 未 online（见 .tmp/paper.out.log）" }
    $results.inert = Invoke-Driver "inert"
    $results.filetree = Invoke-Driver "filetree"
    Stop-Paper

    Write-Host "`n== 相位2 ordering（放行白名单：次序 + 回滚不重放）=="
    Reset-RunDirMirror   # 复位 managed.yml 为 A；DB 覆盖集保留（含命令）
    Start-Paper "beacone2ereload"
    if (-not (Wait-Online)) { Stop-Paper; throw "ordering：agent 未 online" }
    $results.ordering = Invoke-Driver "ordering"

    Write-Host "`n== 相位3 failstatic（杀控制面，文件不动命令不发）=="
    Stop-BeaconCp
    Start-Sleep -Seconds 3
    $results.failstatic = Invoke-Driver "failstatic"
    Stop-Paper

    Write-Host "`n== 收尾：重启控制面（便于复跑）=="
    Start-BeaconCp
}
finally {
    Stop-Paper
}

Write-Host "`n===== E2E 汇总 ====="
$fail = 0
foreach ($k in $results.Keys) {
    $rc = $results[$k]
    if ($rc -eq 0) { $tag = "PASS" } else { $tag = "FAIL($rc)"; $fail++ }
    Write-Host ("  {0,-12} {1}" -f $k, $tag)
}
if ($fail -gt 0) { Write-Host "E2E 失败：$fail 个相位未过"; exit 1 }
Write-Host "E2E 全部相位通过"
exit 0

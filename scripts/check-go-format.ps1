param(
    [string]$LocalPrefix = "github.com/wcpe/Beacon"
)

$ErrorActionPreference = "Stop"

function Resolve-Goimports {
    $goimports = Get-Command goimports -ErrorAction SilentlyContinue
    if ($goimports) {
        return $goimports.Source
    }

    Write-Host "未装 goimports，正在安装..."
    & go install golang.org/x/tools/cmd/goimports@latest
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }

    $goPath = (& go env GOPATH).Trim()
    $candidate = Join-Path $goPath "bin\goimports.exe"
    if (Test-Path -LiteralPath $candidate -PathType Leaf) {
        return $candidate
    }

    return Join-Path $goPath "bin\goimports"
}

function Invoke-FormatList {
    param(
        [string]$Label,
        [string]$Command,
        [string[]]$Arguments,
        [string[]]$Files,
        [string]$TempRoot
    )

    Write-Host $Label
    $allOutput = @()
    $batchSize = 40

    for ($offset = 0; $offset -lt $Files.Count; $offset += $batchSize) {
        $end = [Math]::Min($offset + $batchSize - 1, $Files.Count - 1)
        $batch = $Files[$offset..$end]

        $utf8NoBom = [System.Text.UTF8Encoding]::new($false)
        $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
        $startInfo.FileName = $Command
        foreach ($argument in $Arguments) {
            [void]$startInfo.ArgumentList.Add($argument)
        }
        foreach ($file in $batch) {
            [void]$startInfo.ArgumentList.Add($file)
        }
        $startInfo.RedirectStandardOutput = $true
        $startInfo.RedirectStandardError = $true
        $startInfo.StandardOutputEncoding = $utf8NoBom
        $startInfo.StandardErrorEncoding = $utf8NoBom
        $startInfo.UseShellExecute = $false
        $startInfo.CreateNoWindow = $true

        $process = [System.Diagnostics.Process]::Start($startInfo)
        $stdout = $process.StandardOutput.ReadToEnd()
        $stderr = $process.StandardError.ReadToEnd()
        $process.WaitForExit()

        if ($process.ExitCode -ne 0) {
            throw "格式命令失败：$Command $($Arguments -join ' ') $stderr"
        }

        $allOutput += $stdout.Split([Environment]::NewLine, [StringSplitOptions]::RemoveEmptyEntries)
    }

    $bad = @()
    foreach ($line in $allOutput) {
        $text = $line.Trim()
        if (-not $text) {
            continue
        }

        $relative = [System.IO.Path]::GetRelativePath($TempRoot, $text).Replace('\', '/')
        $bad += $relative
    }

    return $bad
}

$repoRoot = (& git rev-parse --show-toplevel).Trim()
Set-Location $repoRoot

# 本次改动的 .go 文件：工作树相对 HEAD 的差异（含已暂存与未暂存）+ 未跟踪非忽略新文件。
$files = @(
    & git diff --name-only HEAD -- '*.go'
    & git ls-files --others --exclude-standard '*.go'
) | Where-Object { $_ -and (Test-Path -LiteralPath $_ -PathType Leaf) } | Sort-Object -Unique

if ($files.Count -eq 0) {
    Write-Host "无改动的 Go 文件，跳过格式校验。"
    exit 0
}

$goimportsPath = Resolve-Goimports
$utf8 = [System.Text.UTF8Encoding]::new($false, $true)
$tempRoot = Join-Path $repoRoot ".tmp/go-format-check/$([Guid]::NewGuid().ToString('N'))"
$tempFiles = @()

try {
    foreach ($file in $files) {
        $sourcePath = Join-Path $repoRoot $file
        $targetPath = Join-Path $tempRoot $file
        $targetDir = Split-Path -Parent $targetPath
        New-Item -ItemType Directory -Force -Path $targetDir | Out-Null

        $content = [System.IO.File]::ReadAllText($sourcePath, $utf8).Replace("`r", "")
        [System.IO.File]::WriteAllText($targetPath, $content, [System.Text.UTF8Encoding]::new($false))
        $tempFiles += $targetPath
    }

    $gofmtBad = Invoke-FormatList `
        -Label "==== gofmt 格式校验（CRLF 安全，仅本次改动）====" `
        -Command "gofmt" `
        -Arguments @("-l") `
        -Files $tempFiles `
        -TempRoot $tempRoot

    $goimportsBad = Invoke-FormatList `
        -Label "==== goimports 导入分组校验（CRLF 安全，仅本次改动）====" `
        -Command $goimportsPath `
        -Arguments @("-local", $LocalPrefix, "-l") `
        -Files $tempFiles `
        -TempRoot $tempRoot

    $bad = $false
    foreach ($file in $gofmtBad) {
        Write-Host "未通过 gofmt: $file"
        $bad = $true
    }
    foreach ($file in $goimportsBad) {
        Write-Host "未通过 goimports: $file"
        $bad = $true
    }

    if ($bad) {
        Write-Host "存在未通过格式校验的 Go 文件（上方列出），请用 gofmt/goimports 修正。"
        exit 1
    }
} finally {
    if (Test-Path -LiteralPath $tempRoot) {
        Remove-Item -LiteralPath $tempRoot -Recurse -Force
    }
}

Write-Host "gofmt + goimports 全部通过（本次改动）。"

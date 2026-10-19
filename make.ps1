<#
.SYNOPSIS
    order-sys 的 Windows PowerShell 7 开发入口。

.DESCRIPTION
    本脚本只包装日常开发高频目标，职责边界如下：

    - Makefile 仍是 CI 与 Debian 部署的唯一事实来源；本脚本不复制其全部逻辑，
      避免两套定义长期漂移。低频目标（lint/security/sbom/docker-build）直接
      打印 Makefile 中的等价命令，由开发者自行执行或交给 CI。
    - 所有命令都以 `backend/` 为 Go 工作目录（与 Makefile 一致），因为 go.mod
      位于 backend/，在仓库根执行 go 命令会找不到模块。
    - 需要 bash 语义的目标（fmt-check 的 awk 判定、lint 的多行 shell 分支）
      用 PowerShell 原生实现，不依赖 Git Bash，这是 CLAUDE.md「命令必须能在
      PowerShell 7 直接执行」的硬性要求。

.PARAMETER Target
    目标名，取值见 `.\make.ps1 help`。默认 help。

.PARAMETER Config
    run-api / run-worker 使用的 YAML 配置文件路径。留空时沿用 .env 中的
    CONFIG_FILE；两者都为空则只用默认值 + 环境变量启动。

.EXAMPLE
    .\make.ps1 setup

    生成根目录 .env 并写入随机密钥；已有 .env 时跳过，不覆盖。

.EXAMPLE
    .\make.ps1 run-api -Config backend/config/local.example.yaml

    指定配置文件启动 API（.env 中的 CONFIG_FILE 优先级低于显式参数）。

.NOTES
    执行策略若阻止脚本运行，可临时放行：
        Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass
    或仅对本次调用放行：
        pwsh -ExecutionPolicy Bypass -File .\make.ps1 help
#>

[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [string]$Target = 'help',

    [Parameter()]
    [string]$Config
)

# 遇到未处理异常立即终止，避免目标失败后继续执行后续步骤。
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
# go/git/docker 的 stderr 是普通诊断信息（如编译错误、git 警告），不应被
# PowerShell 7 当成终止性错误；退出码统一由 $LASTEXITCODE 判断。
$PSNativeCommandUseErrorActionPreference = $false

# 输出被重定向（管道、CI、`Out-File`）时，PowerShell 默认按系统 ANSI 代码页
# （中文 Windows 为 GBK）编码，消费方按 UTF-8 读取就会乱码。仅在重定向时切到
# UTF-8，交互式终端保持系统默认，避免在传统 cmd 控制台出现反向乱码。
if ([Console]::IsOutputRedirected) {
    [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
}

# 以脚本所在目录为仓库根，保证从任意当前目录调用都能正确解析相对路径。
# 这里不用 $PSScriptRoot 直接拼字符串，是为了同时兼容 Windows 与 Debian 路径分隔符。
$RepoRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$Backend = Join-Path $RepoRoot 'backend'
$EnvFile = Join-Path $RepoRoot '.env'

# 与 Makefile 的 LDFLAGS 保持一致：版本信息在构建期注入 version 包。
# 取不到值时退化为 dev/unknown，保证首次 clone 未打 tag 时也能构建。
function Get-BuildLdflags {
    $version = (git describe --tags --always --dirty 2>$null)
    if (-not $version) { $version = 'dev' }
    $commit = (git rev-parse --short HEAD 2>$null)
    if (-not $commit) { $commit = 'unknown' }
    $buildDate = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')

    return @(
        "-X github.com/vevila2020/order-sys/internal/version.Version=$version"
        "-X github.com/vevila2020/order-sys/internal/version.Commit=$commit"
        "-X github.com/vevila2020/order-sys/internal/version.BuildDate=$buildDate"
    ) -join ' '
}

# 统一的失败出口：非 0 退出码便于在 CI 或脚本组合中使用。
function Exit-Failure([string]$Message) {
    Write-Host $Message -ForegroundColor Red
    exit 1
}

# 运行 go 命令并只返回退出码。
# 不能写成 `& go @args; return $LASTEXITCODE`：函数体中的原生命令 stdout 会被
# 一并捕获为函数返回值，导致调用处拿到 "测试输出+退出码" 的数组。这里用
# Out-Host 让子进程输出直通控制台，只把退出码作为返回值。
function Invoke-Go {
    Push-Location $Backend
    try {
        & go @args | Out-Host
        return $LASTEXITCODE
    }
    finally {
        Pop-Location
    }
}

function Get-EnvMap {
    if (-not (Test-Path $EnvFile)) {
        return @{}
    }
    $map = @{}
    foreach ($line in Get-Content -LiteralPath $EnvFile -Encoding utf8) {
        $trimmed = $line.Trim()
        # 跳过空行与注释；只处理 KEY=VALUE，避免把格式错误的行当成配置。
        if ($trimmed -eq '' -or $trimmed.StartsWith('#')) { continue }
        $idx = $trimmed.IndexOf('=')
        if ($idx -lt 1) { continue }
        $key = $trimmed.Substring(0, $idx).Trim()
        $value = $trimmed.Substring($idx + 1).Trim()
        # bash 的 `source` 会剥掉成对引号（DSN 等含 shell 元字符的值必须带引号），
        # 这里做同样处理，保证 PS 与 Makefile 看到的值一致。
        if ($value.Length -ge 2 -and (
                ($value.StartsWith('"') -and $value.EndsWith('"')) -or
                ($value.StartsWith("'") -and $value.EndsWith("'")))) {
            $value = $value.Substring(1, $value.Length - 2)
        }
        $map[$key] = $value
    }
    return $map
}

function Invoke-WithEnv {
    param(
        [hashtable]$EnvMap,
        [scriptblock]$Action
    )
    $saved = @{}
    try {
        foreach ($key in $EnvMap.Keys) {
            $saved[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
            [Environment]::SetEnvironmentVariable($key, $EnvMap[$key], 'Process')
        }
        # 同上：Action 内部的原生命令输出必须直通控制台，不能混入返回值。
        & $Action | Out-Host
        return $LASTEXITCODE
    }
    finally {
        # 恢复进程环境，避免同一会话内后续调用被上一次的 .env 污染。
        foreach ($key in $saved.Keys) {
            [Environment]::SetEnvironmentVariable($key, $saved[$key], 'Process')
        }
    }
}

function Invoke-Setup {
    if (Test-Path $EnvFile) {
        Write-Host '.env already exists, skipping' -ForegroundColor Yellow
        return 0
    }

    $template = Join-Path $Backend '.env.example'
    if (-not (Test-Path $template)) {
        Exit-Failure "template not found: $template"
    }

    # 用 .NET 密码学随机数生成密钥，不依赖 openssl 或 /dev/urandom，
    # 保证在纯 PowerShell 7 环境下可用（Makefile 中的做法依赖 GNU 工具链）。
    # JWT 至少 32 字节；这里取 32 字节 → 64 个十六进制字符。
    $jwtBytes = [byte[]]::new(32)
    [System.Security.Cryptography.RandomNumberGenerator]::Fill($jwtBytes)
    $jwtSecret = [Convert]::ToHexString($jwtBytes).ToLowerInvariant()

    $tokenBytes = [byte[]]::new(24)
    [System.Security.Cryptography.RandomNumberGenerator]::Fill($tokenBytes)
    $callbackToken = [Convert]::ToHexString($tokenBytes).ToLowerInvariant()

    # 逐行替换，保留模板中的注释与顺序。
    # 关键约束：
    # - 换行必须显式用 LF。Set-Content 在 Windows 上默认写 CRLF，而 Makefile
    #   通过 bash 的 `source .env` 加载，CRLF 会让每个值尾部带上 \r（例如
    #   APP_ENV 变成 "local\r"），导致配置匹配失败且极难排查。
    # - 编码用 UTF-8 无 BOM：BOM 会被 sh 当成第一个变量名的一部分。
    $lines = Get-Content -LiteralPath $template -Encoding utf8
    $out = foreach ($line in $lines) {
        if ($line -match '^JWT_SECRET=') { "JWT_SECRET=$jwtSecret" }
        elseif ($line -match '^PAYMENT_CALLBACK_TOKEN=') { "PAYMENT_CALLBACK_TOKEN=$callbackToken" }
        else { $line }
    }
    [System.IO.File]::WriteAllText($EnvFile, ($out -join "`n") + "`n", [System.Text.UTF8Encoding]::new($false))

    Write-Host '.env created with random secrets' -ForegroundColor Green
    return 0
}

function Invoke-Fmt {
    $code = Invoke-Go fmt ./...
    if ($code -ne 0) { return $code }
    # gofmt -w . 与 Makefile 一致，覆盖 go fmt 不处理的非包目录文件。
    & gofmt -w $Backend
    return $LASTEXITCODE
}

function Invoke-FmtCheck {
    Push-Location $Backend
    try {
        $out = & gofmt -l .
        if ($LASTEXITCODE -ne 0) { return $LASTEXITCODE }
    }
    finally {
        Pop-Location
    }
    # gofmt -l 的输出已带相对 backend/ 的路径，这里补全前缀便于直接点击跳转。
    $files = @($out | Where-Object { $_ -ne '' })
    if ($files.Count -gt 0) {
        Write-Host 'unformatted files:' -ForegroundColor Red
        $files | ForEach-Object { Write-Host "  backend/$_" }
        return 1
    }
    Write-Host 'format check passed' -ForegroundColor Green
    return 0
}

function Invoke-Build {
    $ldflags = Get-BuildLdflags
    Push-Location $Backend
    try {
        & go build -trimpath -ldflags $ldflags -o bin/api ./cmd/api | Out-Host
        if ($LASTEXITCODE -ne 0) { return $LASTEXITCODE }
        & go build -trimpath -ldflags $ldflags -o bin/worker ./cmd/worker | Out-Host
        return $LASTEXITCODE
    }
    finally {
        Pop-Location
    }
}

# 把 -Config 解析为绝对路径。
# 进程会 cd 到 backend/ 再启动，而用户通常按仓库根给出相对路径
# （如 backend/config/local.example.yaml），直接透传会变成
# backend/backend/config/... 而报 "cannot find the path"。
function Resolve-ConfigPath([string]$ConfigPath) {
    if (-not $ConfigPath) { return '' }
    if ([System.IO.Path]::IsPathRooted($ConfigPath)) { return $ConfigPath }
    return [System.IO.Path]::GetFullPath((Join-Path (Get-Location).Path $ConfigPath))
}

function Invoke-RunApi([string]$ConfigPath) {
    $envMap = Get-EnvMap
    if ($ConfigPath) {
        # 显式参数优先于 .env，符合 PowerShell 用户对命令行参数的直觉。
        $envMap['CONFIG_FILE'] = $ConfigPath
    }
    $ldflags = Get-BuildLdflags
    Push-Location $Backend
    try {
        return Invoke-WithEnv -EnvMap $envMap -Action {
            & go run -ldflags $ldflags ./cmd/api
            $LASTEXITCODE
        }
    }
    finally {
        Pop-Location
    }
}

function Invoke-RunWorker([string]$ConfigPath) {
    $envMap = Get-EnvMap
    if ($ConfigPath) {
        $envMap['CONFIG_FILE'] = $ConfigPath
    }
    $ldflags = Get-BuildLdflags
    Push-Location $Backend
    try {
        return Invoke-WithEnv -EnvMap $envMap -Action {
            & go run -ldflags $ldflags ./cmd/worker
            $LASTEXITCODE
        }
    }
    finally {
        Pop-Location
    }
}

# 低频目标不在本脚本中重新实现，只给出 Makefile 的等价命令。
# 这样 CI/部署行为始终以 Makefile 为准，不会因为本地包装脚本产生差异。
function Show-Delegated([string]$Name) {
    $hint = switch ($Name) {
        'lint'         { 'make lint         # 需先安装 golangci-lint' }
        'security'     { 'make security     # 需 govulncheck 与 gitleaks' }
        'sbom'         { 'make sbom         # 需 syft' }
        'test-race'    { 'make test-race    # 需要 CGO 与 C 编译器' }
        'docker-build' { 'make docker-build # 需要 Docker daemon' }
        'ci'           { 'make ci           # 与 CI 等同的完整门禁' }
        default        { "make $Name" }
    }
    Write-Host "target '$Name' is owned by the Makefile; run:" -ForegroundColor Yellow
    Write-Host "  $hint"
    return 0
}

function Invoke-Help {
    # 用有序字典而不是嵌套数组：PowerShell 会把 `@(@('a','b'))` 展开成一维字符串，
    # 之后 $row[0] 取到的是字符而非目标名。
    $rows = [ordered]@{
        'help'            = '列出所有目标'
        'setup'           = '生成 .env（随机密钥，不覆盖已有文件）'
        'up'              = '启动依赖与应用（docker compose）'
        'down'            = '停止服务（保留数据卷）'
        'logs'            = '跟踪 api/worker 日志'
        'ps'              = '查看服务状态'
        'fmt'             = '格式化代码'
        'fmt-check'       = '检查格式（CI 门禁）'
        'vet'             = 'go vet'
        'test'            = '单元测试'
        'build'           = '构建 api 与 worker 到 backend/bin'
        'run-api'         = '本地运行 API（读取根目录 .env）'
        'run-worker'      = '本地运行 worker（读取根目录 .env）'
        'clean'           = '清理构建产物（不动数据卷）'
        'lint/security/sbom/test-race/ci/docker-build' = '委托给 Makefile'
    }
    Write-Host 'order-sys (PowerShell 7) targets:'
    foreach ($name in $rows.Keys) {
        Write-Host ('  {0,-46} {1}' -f $name, $rows[$name]) -ForegroundColor Cyan
    }
    Write-Host ''
    Write-Host 'usage: .\make.ps1 <target> [-Config <yaml path>]'
    return 0
}

# 统一执行 docker compose 子命令：输出直通控制台，只把退出码向上传递。
# 与 Invoke-Go 同理，避免 `& docker ...; $LASTEXITCODE` 把 stdout 混入返回值。
function Invoke-Compose {
    & docker compose @args | Out-Host
    return $LASTEXITCODE
}

$exitCode = switch ($Target.ToLowerInvariant()) {
    'help' { Invoke-Help }

    'setup' { Invoke-Setup }

    'up' {
        $code = Invoke-Compose up -d --build
        if ($code -eq 0) {
            Write-Host 'api:      http://localhost:8080/health/live'
            Write-Host 'grafana:  http://localhost:3000'
            Write-Host 'jaeger:   http://localhost:16686'
        }
        $code
    }

    'down' { Invoke-Compose down }
    'logs' { Invoke-Compose logs -f api worker }
    'ps'   { Invoke-Compose ps }

    'fmt'       { Invoke-Fmt }
    'fmt-check' { Invoke-FmtCheck }
    'vet'       { Invoke-Go vet ./... }
    'test'      { Invoke-Go test ./... -count=1 }
    'build'     { Invoke-Build }
    'run-api'   { Invoke-RunApi (Resolve-ConfigPath $Config) }
    'run-worker' { Invoke-RunWorker (Resolve-ConfigPath $Config) }

    'clean' {
        foreach ($path in @('backend/bin', 'artifacts', 'coverage.out')) {
            $full = Join-Path $RepoRoot $path
            if (-not (Test-Path $full)) { continue }
            try {
                Remove-Item -LiteralPath $full -Recurse -Force
                Write-Host "removed $path"
            }
            catch {
                # Windows 上正在运行的 api/worker 会锁住 exe，Go 还会把旧文件重命名为
                # `api~`。这类文件物理上删不掉，给出可操作提示但不让 clean 失败，
                # 否则本地开发每跑一次 clean 都会中断。
                Write-Host "skipped $path (in use; stop make.ps1 run-api/run-worker first)" -ForegroundColor Yellow
            }
        }
        0
    }

    { $_ -in @('lint', 'security', 'sbom', 'test-race', 'docker-build', 'ci') } {
        Show-Delegated $Target.ToLowerInvariant()
    }

    default {
        Write-Host "unknown target: $Target" -ForegroundColor Red
        Write-Host 'run .\make.ps1 help for the target list'
        1
    }
}

exit $exitCode

<#
.SYNOPSIS
    为本地开发建立 / 停止 / 查看到远程 MySQL 的 SSH 隧道。

.DESCRIPTION
    用途：Windows 本地开发时，把远程服务器的 MySQL 映射到本机端口，
    Go 进程通过 127.0.0.1:<LocalPort> 访问，无需把 3306 暴露到公网。

    认证方式：
    - 默认使用 ssh-agent。私钥带 passphrase 时必须先用 ssh-add 加载，
      否则本脚本在 BatchMode 下无法应答口令，会直接失败。
    - 也可用 -KeyFile 指定私钥；若该私钥有 passphrase，仍然需要 agent。

    安全约束：
    - 隧道只绑定 127.0.0.1，局域网内其他机器无法使用该端口。
    - 本脚本不接收、不存储、不打印数据库密码；DSN 仍然来自 .env。
    - 私钥路径只作为命令行参数传入，不写入仓库内的任何文件。

.PARAMETER Action
    Start（默认）/ Stop / Status。

.PARAMETER RemoteHost
    跳板机（同时也是 MySQL 所在主机）的地址。

.PARAMETER RemotePort
    SSH 端口。默认 22。

.PARAMETER LocalPort
    本机监听端口。默认 13306，避开本地 MySQL 的 3306。

.PARAMETER RemoteDbHost
    从服务器视角看到的 MySQL 地址。默认 127.0.0.1（MySQL 与 SSH 同机）。

.PARAMETER RemoteDbPort
    MySQL 端口。默认 3306。

.PARAMETER SshUser
    SSH 登录用户。默认 root。

.PARAMETER KeyFile
    可选。私钥路径；不指定时依赖 ssh-agent。

.EXAMPLE
    # 首次：把私钥加入 agent（会提示输入 passphrase，只需一次）
    ssh-add C:\LGF\oracleK\vps-rn

    # 启动隧道
    .\scripts\ssh-tunnel.ps1 -Action Start -RemoteHost 23.238.26.42 -RemotePort 2233

.EXAMPLE
    .\scripts\ssh-tunnel.ps1 -Action Status -RemoteHost 23.238.26.42 -RemotePort 2233

.EXAMPLE
    .\scripts\ssh-tunnel.ps1 -Action Stop
#>

[CmdletBinding()]
param(
    [ValidateSet('Start', 'Stop', 'Status')]
    [string]$Action = 'Start',

    [Parameter(Mandatory = $true)]
    [string]$RemoteHost,

    [int]$RemotePort = 22,
    [int]$LocalPort = 13306,
    [string]$RemoteDbHost = '127.0.0.1',
    [int]$RemoteDbPort = 3306,
    [string]$SshUser = 'root',
    [string]$KeyFile
)

$ErrorActionPreference = 'Stop'
# ssh 的诊断信息走 stderr，不应被 PowerShell 当成终止性错误。
$PSNativeCommandUseErrorActionPreference = $false

# 用“主机+本地端口”生成 PID 文件名，允许同时开多条隧道连不同环境。
$StateDir = Join-Path ([System.IO.Path]::GetTempPath()) 'order-sys-tunnels'
$StateFile = Join-Path $StateDir ("ssh-{0}-{1}.pid" -f $RemoteHost, $LocalPort)

function Get-TunnelProcess {
    if (-not (Test-Path $StateFile)) { return $null }
    $raw = (Get-Content -LiteralPath $StateFile -Raw).Trim()
    if (-not $raw) { return $null }
    $proc = Get-Process -Id ([int]$raw) -ErrorAction SilentlyContinue
    if ($null -eq $proc) { return $null }
    return $proc
}

function Test-LocalPortOpen {
    param([int]$Port)
    try {
        $client = [System.Net.Sockets.TcpClient]::new()
        $connected = $client.ConnectAsync('127.0.0.1', $Port).Wait(1000)
        $client.Close()
        return $connected
    }
    catch {
        return $false
    }
}

function Start-Tunnel {
    $existing = Get-TunnelProcess
    if ($null -ne $existing) {
        Write-Host "tunnel already running (pid $($existing.Id))" -ForegroundColor Yellow
        Write-Host "  127.0.0.1:$LocalPort -> $RemoteHost`:$RemotePort -> $RemoteDbHost`:$RemoteDbPort"
        return 0
    }

    # 端口被别的程序占用时直接报错，而不是让 ssh 静默失败。
    if (Test-LocalPortOpen -Port $LocalPort) {
        Write-Host "local port $LocalPort is already in use by another process" -ForegroundColor Red
        Write-Host "choose a different -LocalPort" -ForegroundColor Yellow
        return 1
    }

    $sshArgs = @(
        '-N'
        '-o', 'ExitOnForwardFailure=yes'
        # 定期发心跳，防止 NAT/防火墙静默断开后隧道变成"假活"。
        '-o', 'ServerAliveInterval=30'
        '-o', 'ServerAliveCountMax=3'
        # BatchMode 禁止任何交互提示：口令缺失时立刻失败，而不是挂住等输入。
        '-o', 'BatchMode=yes'
        '-p', "$RemotePort"
        # 只绑回环地址，避免把数据库端口暴露给局域网。
        '-L', "127.0.0.1:$LocalPort`:$RemoteDbHost`:$RemoteDbPort"
        "$SshUser@$RemoteHost"
    )
    if ($KeyFile) {
        $sshArgs = @('-i', $KeyFile) + $sshArgs
    }

    $proc = Start-Process -FilePath 'ssh' -ArgumentList $sshArgs -PassThru -NoNewWindow
    New-Item -ItemType Directory -Force -Path $StateDir | Out-Null
    Set-Content -LiteralPath $StateFile -Value $proc.Id -NoNewline

    # ssh 建立转发需要一点时间；这里轮询而不是固定 sleep，失败能尽早暴露。
    $deadline = (Get-Date).AddSeconds(10)
    while ((Get-Date) -lt $deadline) {
        Start-Sleep -Milliseconds 250
        if ($proc.HasExited) {
            Remove-Item -LiteralPath $StateFile -ErrorAction SilentlyContinue
            # Start-Process -PassThru 返回的 Process 对象拿不到退出码（读出来是
            # 空值），因此这里只报告"已退出"，具体原因由 ssh 自身的 stderr 给出。
            Write-Host 'ssh exited before the tunnel was established' -ForegroundColor Red
            Write-Host '  常见原因：私钥未加入 ssh-agent、服务器拒绝该公钥、端口被占用。' -ForegroundColor Yellow
            Write-Host '  排查：ssh-add -l 查看已加载密钥；ssh -v 查看认证过程。' -ForegroundColor Yellow
            return 1
        }
        if (Test-LocalPortOpen -Port $LocalPort) {
            Write-Host "tunnel started (pid $($proc.Id))" -ForegroundColor Green
            Write-Host "  127.0.0.1:$LocalPort -> $RemoteHost`:$RemotePort -> $RemoteDbHost`:$RemoteDbPort"
            Write-Host ''
            Write-Host '在 .env 中使用（注意必须是 127.0.0.1，不能用 localhost）：' -ForegroundColor Cyan
            Write-Host "  MYSQL_DSN=`"dev:<密码>@tcp(127.0.0.1:$LocalPort)/order_sys?parseTime=true&loc=UTC`""
            return 0
        }
    }

    Write-Host 'tunnel process started but the local port never opened' -ForegroundColor Red
    Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $StateFile -ErrorAction SilentlyContinue
    return 1
}

function Stop-Tunnel {
    $proc = Get-TunnelProcess
    if ($null -eq $proc) {
        Write-Host 'no tunnel is running' -ForegroundColor Yellow
        Remove-Item -LiteralPath $StateFile -ErrorAction SilentlyContinue
        return 0
    }
    Stop-Process -Id $proc.Id -Force
    Remove-Item -LiteralPath $StateFile -ErrorAction SilentlyContinue
    Write-Host "tunnel stopped (pid $($proc.Id))" -ForegroundColor Green
    return 0
}

function Show-Status {
    $proc = Get-TunnelProcess
    if ($null -eq $proc) {
        Write-Host 'tunnel is not running' -ForegroundColor Yellow
        return 0
    }
    $portState = if (Test-LocalPortOpen -Port $LocalPort) { 'listening' } else { 'NOT listening' }
    Write-Host "tunnel running (pid $($proc.Id)), local port $LocalPort : $portState" -ForegroundColor Green
    Write-Host "  $RemoteHost`:$RemotePort -> $RemoteDbHost`:$RemoteDbPort"
    return 0
}

# 先用变量接住返回值再 exit：写成 `exit (Start-Tunnel)` 时，
# 函数内部 Write-Host 的输出与返回值混在一起，可能让 exit 收到非整数。
$code = switch ($Action) {
    'Start' { Start-Tunnel }
    'Stop' { Stop-Tunnel }
    'Status' { Show-Status }
}
exit ($code -as [int])

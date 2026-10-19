# Installs the zbox CLI from its GitHub release: irm https://box.zerops.io/install.ps1 | iex
#
#   ZBOX_VERSION      release tag to install (v0.24), the latest release when unset
#   ZBOX_INSTALL_DIR  where the binary goes, $env:LOCALAPPDATA\Programs\zbox by default
$ErrorActionPreference = 'Stop'

$repo = 'zeropsio/zbox-cli'
$base = if ($env:ZBOX_RELEASE_BASE) { $env:ZBOX_RELEASE_BASE } else { "https://github.com/$repo/releases" }

$arch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
    'X64'   { 'amd64' }
    'Arm64' { 'arm64' }
    default { throw "unsupported architecture $_" }
}
$file = "zbox-windows-$arch.exe"
$url = if ($env:ZBOX_VERSION) { "$base/download/$env:ZBOX_VERSION" } else { "$base/latest/download" }

$dir = if ($env:ZBOX_INSTALL_DIR) { $env:ZBOX_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\zbox' }
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Write-Host "downloading $file$(if ($env:ZBOX_VERSION) { " $env:ZBOX_VERSION" })..."
    Invoke-WebRequest -Uri "$url/$file" -OutFile (Join-Path $tmp 'zbox.exe') -UseBasicParsing
    Invoke-WebRequest -Uri "$url/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt') -UseBasicParsing

    $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match "\s$([regex]::Escape($file))$" }
    if (-not $line) { throw "$file is not in checksums.txt" }
    $want = ($line -split '\s+')[0]
    $have = (Get-FileHash (Join-Path $tmp 'zbox.exe') -Algorithm SHA256).Hash
    if ($have -ne $want) { throw "checksum mismatch for $file" }

    New-Item -ItemType Directory -Path $dir -Force | Out-Null
    Move-Item -Path (Join-Path $tmp 'zbox.exe') -Destination (Join-Path $dir 'zbox.exe') -Force
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

$exe = Join-Path $dir 'zbox.exe'
Write-Host "installed $(& $exe --version) to $exe"

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $dir) {
    [Environment]::SetEnvironmentVariable('Path', "$dir;$userPath", 'User')
    $env:Path = "$dir;$env:Path"
    Write-Host "added $dir to your PATH, open a new terminal for it to apply"
}

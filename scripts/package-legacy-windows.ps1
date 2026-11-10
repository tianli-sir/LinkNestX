param(
  [Parameter(Mandatory = $true)][ValidateSet('x86', 'x64')][string]$Architecture,
  [Parameter(Mandatory = $true)][string]$NativeExecutable,
  [string]$UiDirectory,
  [string]$OutputDirectory,
  [string]$Makensis = 'makensis.exe'
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$legacyRoot = Join-Path $root 'client/legacy'
if (-not $UiDirectory) { $UiDirectory = Join-Path $root 'client/dist' }
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $root 'dist/legacy-windows' }
$NativeExecutable = (Resolve-Path $NativeExecutable).Path
$UiDirectory = (Resolve-Path $UiDirectory).Path
$package = Get-Content (Join-Path $legacyRoot 'package.json') -Raw | ConvertFrom-Json
$version = $package.version
$electronVersion = $package.devDependencies.electron
if ($version -notmatch '^\d+\.\d+\.\d+$' -or $electronVersion -notmatch '^22\.\d+\.\d+$') { throw 'Invalid package version.' }
if (-not (Test-Path (Join-Path $UiDirectory 'index.html'))) { throw 'Build the shared frontend first.' }
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
$OutputDirectory = (Resolve-Path $OutputDirectory).Path
$buildRoot = Join-Path $OutputDirectory ([Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $buildRoot | Out-Null
try {
  $electronArch = if ($Architecture -eq 'x86') { 'ia32' } else { 'x64' }
  $archiveName = "electron-v$electronVersion-win32-$electronArch.zip"
  $baseUrl = "https://github.com/electron/electron/releases/download/v$electronVersion"
  $archive = Join-Path $buildRoot $archiveName
  $checksums = Join-Path $buildRoot 'SHASUMS256.txt'
  Invoke-WebRequest -Uri "$baseUrl/SHASUMS256.txt" -OutFile $checksums
  Invoke-WebRequest -Uri "$baseUrl/$archiveName" -OutFile $archive
  $entry = @(Get-Content $checksums | Where-Object { $_ -match ('^[a-fA-F0-9]{64}\s+\*?' + [regex]::Escape($archiveName) + '$') })
  if ($entry.Count -ne 1) { throw 'Official Electron checksum missing or ambiguous.' }
  $expected = ($entry[0] -split '\s+')[0]
  if ((Get-FileHash $archive -Algorithm SHA256).Hash -ne $expected) { throw 'Electron checksum verification failed.' }
  $name = "LinkNestX-$version-windows7-$Architecture"
  $stage = Join-Path $buildRoot $name
  Expand-Archive -Path $archive -DestinationPath $stage
  Move-Item (Join-Path $stage 'electron.exe') (Join-Path $stage 'LinkNestX-Legacy.exe')
  Remove-Item (Join-Path $stage 'resources/default_app.asar') -ErrorAction SilentlyContinue
  $appRoot = Join-Path $stage 'resources/app'
  $nativeRoot = Join-Path $stage 'resources/native'
  New-Item -ItemType Directory -Path $appRoot, $nativeRoot -Force | Out-Null
  Copy-Item (Join-Path $legacyRoot 'shell') (Join-Path $appRoot 'shell') -Recurse
  Copy-Item $UiDirectory (Join-Path $appRoot 'ui') -Recurse
  Copy-Item (Join-Path $legacyRoot 'package.json') (Join-Path $appRoot 'package.json')
  Copy-Item (Join-Path $root 'client/src-tauri/icons/icon.png') (Join-Path $appRoot 'icon.png')
  Copy-Item $NativeExecutable (Join-Path $nativeRoot 'linknestx-legacy-native.exe')
  Copy-Item (Join-Path $root 'LICENSE') (Join-Path $stage 'LICENSE-LinkNestX.txt')
  Copy-Item (Join-Path $root 'docs/windows-compatibility.md') (Join-Path $stage 'WINDOWS-COMPATIBILITY.md')
  & node (Join-Path $legacyRoot 'tools/audit-pe.cjs') (Join-Path $nativeRoot 'linknestx-legacy-native.exe') $Architecture (Join-Path $stage 'native-imports.json')
  if ($LASTEXITCODE -ne 0) { throw 'Native Win7 import audit failed.' }
  $commit = (& git -C $root rev-parse HEAD).Trim()
  if ($LASTEXITCODE -ne 0) { throw 'Cannot determine source commit.' }
  @{ version = $version; architecture = $Architecture; minimumOS = 'Windows 7 SP1'; source = "https://github.com/tianli-sir/LinkNestX/tree/$commit"; electron = $electronVersion; electronArchiveSHA256 = $expected; nativeSHA256 = (Get-FileHash $NativeExecutable -Algorithm SHA256).Hash; signed = $false; updates = 'manual-full-package' } | ConvertTo-Json | Set-Content -Path (Join-Path $stage 'BUILD-INFO.json') -Encoding utf8
  $zip = Join-Path $OutputDirectory "$name.zip"
  $installer = Join-Path $OutputDirectory "$name-setup.exe"
  if ((Test-Path $zip) -or (Test-Path $installer)) { throw 'Output already exists; choose a new output directory.' }
  $uninstallList = Join-Path $buildRoot 'uninstall-files.nsh'
  $uninstallLines = @()
  Get-ChildItem $stage -File -Recurse | ForEach-Object {
    $relative = $_.FullName.Substring($stage.Length + 1).Replace('$', '$$')
    $uninstallLines += 'Delete "$INSTDIR\' + $relative + '"'
  }
  Get-ChildItem $stage -Directory -Recurse | Sort-Object { $_.FullName.Length } -Descending | ForEach-Object {
    $relative = $_.FullName.Substring($stage.Length + 1).Replace('$', '$$')
    $uninstallLines += 'RMDir "$INSTDIR\' + $relative + '"'
  }
  $uninstallLines | Set-Content -Path $uninstallList -Encoding utf8
  & $Makensis /V2 "/DAPP_STAGE=$stage" "/DAPP_OUTPUT=$installer" "/DAPP_UNINSTALL_LIST=$uninstallList" "/DAPP_VERSION=$version" "/DAPP_ARCH=$Architecture" (Join-Path $legacyRoot 'installer/legacy.nsi')
  if ($LASTEXITCODE -ne 0 -or -not (Test-Path $installer)) { throw 'Installer build failed.' }
  Compress-Archive -Path $stage -DestinationPath $zip -CompressionLevel Optimal
  @($zip, $installer) | ForEach-Object { $hash = Get-FileHash $_ -Algorithm SHA256; ($hash.Hash.ToLowerInvariant() + '  ' + (Split-Path $_ -Leaf)) } | Set-Content -Path (Join-Path $OutputDirectory "$name-SHA256SUMS.txt") -Encoding ascii
  Write-Host "Created offline installer and portable package: $name"
} finally {
  if (Test-Path $buildRoot) { Remove-Item $buildRoot -Recurse -Force }
}

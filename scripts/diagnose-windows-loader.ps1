param(
  [string]$BinaryDirectory = 'client/src-tauri/target/debug/deps',
  [string]$Output = '.cache/windows-loader-diagnostics.json'
)
$ErrorActionPreference = 'Stop'
if (-not $IsWindows) { throw 'This diagnostic must run on Windows.' }
if (-not (Test-Path -LiteralPath $BinaryDirectory)) { Write-Host 'No native test binary directory was generated.'; exit 0 }
$parent = Split-Path -Parent $Output
if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
$importReport = "$Output.imports.json"
& node (Join-Path $PSScriptRoot 'diagnose-windows-imports.cjs') $BinaryDirectory $importReport
if ($LASTEXITCODE -ne 0) { throw 'Static PE inspection failed.' }
$inventory = Get-Content -LiteralPath $importReport -Raw | ConvertFrom-Json
# Load only OS-resolved system DLLs. Never execute the failed EXE, load adjacent
# DLLs, search PATH, or print environment variables. GetProcAddress also follows
# system export forwarders and API-set mappings, unlike a plain export dump.
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class LinkNestXLoaderProbe {
  [DllImport("kernel32.dll", CharSet=CharSet.Unicode, ExactSpelling=true, SetLastError=true)]
  public static extern IntPtr LoadLibraryExW(string name, IntPtr reserved, uint flags);
  [DllImport("kernel32.dll", EntryPoint="GetProcAddress", CharSet=CharSet.Ansi, ExactSpelling=true, SetLastError=true)]
  public static extern IntPtr GetNamedExport(IntPtr module, string name);
  [DllImport("kernel32.dll", EntryPoint="GetProcAddress", ExactSpelling=true, SetLastError=true)]
  public static extern IntPtr GetOrdinalExport(IntPtr module, IntPtr ordinal);
  [DllImport("kernel32.dll")]
  public static extern bool FreeLibrary(IntPtr module);
}
'@
$modules = @{}
$results = [System.Collections.Generic.List[object]]::new()
$probeArchitecture = [Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture.ToString().ToLowerInvariant()
try {
  foreach ($image in $inventory.images) {
    if ($image.diagnosticError) { Write-Host "PE_PARSE_ERROR $($image.file): $($image.diagnosticError)"; continue }
    Write-Host "Inspecting $($image.file) ($($image.architecture), $(@($image.imports).Count) imports)"
    foreach ($import in $image.imports) {
      $dll = $import.dll
      if ($dll -notmatch '^[a-z0-9_.-]+\.(dll|drv)$') { throw 'Invalid DLL basename.' }
      if (-not $modules.ContainsKey($dll)) {
        $handle = [LinkNestXLoaderProbe]::LoadLibraryExW($dll, [IntPtr]::Zero, 0x800)
        $loadError = if ($handle -eq [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::GetLastWin32Error() } else { 0 }
        $modules[$dll] = @{ Handle = $handle; Error = $loadError }
      }
      $module = $modules[$dll]
      $symbol = if ($null -ne $import.ordinal) { "#$($import.ordinal)" } else { $import.symbol }
      $status = 'present'
      if ($image.architecture -ne $probeArchitecture) { $status = 'architecture-not-verified-by-current-probe' }
      elseif ($module.Handle -eq [IntPtr]::Zero) { $status = 'system-dll-load-failed' }
      else {
        $address = if ($null -ne $import.ordinal) {
          [LinkNestXLoaderProbe]::GetOrdinalExport($module.Handle, [IntPtr][int]$import.ordinal)
        } else { [LinkNestXLoaderProbe]::GetNamedExport($module.Handle, $import.symbol) }
        if ($address -eq [IntPtr]::Zero) { $status = 'missing-system-export' }
      }
      $local = @($inventory.adjacent | Where-Object { $_.file.ToLowerInvariant() -eq $dll })
      $adjacentStatus = $null
      if ($local.Count) {
        $matching = @($local[0].exports | Where-Object { if ($null -ne $import.ordinal) { $_.ordinal -eq $import.ordinal } else { $_.names -ccontains $import.symbol } })
        $adjacentStatus = if (-not $matching.Count) { 'missing-adjacent-export' } elseif ($matching[0].forwarder) { 'forwarder:' + $matching[0].forwarder } else { 'present' }
      }
      $row = [ordered]@{ image = $image.file; architecture = $image.architecture; dll = $dll; symbol = $symbol; delayed = [bool]$import.delayed; systemStatus = $status; loadError = $module.Error; adjacentStatus = $adjacentStatus }
      $results.Add($row)
      if ($status -ne 'present' -or $adjacentStatus) { Write-Host "$status image=$($image.file) dll=$dll symbol=$symbol delayed=$($import.delayed) win32=$($module.Error) adjacent=$adjacentStatus" }
    }
  }
  $report = [ordered]@{
    schemaVersion = 1
    osVersion = [Environment]::OSVersion.Version.ToString()
    probeArchitecture = $probeArchitecture
    scope = 'System32 exports, API-set resolution and static adjacent DLL exports. PATH and side-by-side activation are not emulated.'
    parseErrors = @($inventory.images | Where-Object { $_.diagnosticError })
    imports = $results.ToArray()
  }
  $report | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $Output -Encoding utf8
  Write-Host "Import diagnostic complete: $($results.Count) imports. Original test failure remains authoritative."
} finally {
  foreach ($module in $modules.Values) { if ($module.Handle -ne [IntPtr]::Zero) { [void][LinkNestXLoaderProbe]::FreeLibrary($module.Handle) } }
}

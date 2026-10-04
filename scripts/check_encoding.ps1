# Encoding guard: rejects BOM and CRLF in source files.
#
# WHY A SEPARATE SCRIPT instead of "being more careful":
# the BOM/CRLF trap fired three times in one session - twice from PowerShell
# Set-Content -Encoding UTF8 and once from the file-writing tool itself. Each
# time it broke either gofmt -l or the Linux build. Repeating an error means a
# mechanical guard, not a promise to remember.
#
# Deliberately ASCII-only: a guard for encoding damage must not itself be
# able to introduce encoding damage.
#
# Usage:
#   powershell.exe -NoProfile -File scripts\check_encoding.ps1
#   powershell.exe -NoProfile -File scripts\check_encoding.ps1 -Files a.go,b.go
param([string]$Files = "")

$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot -Parent)

# Scope is deliberately narrow: the files staged for commit, plus anything passed
# with -Files. Untracked leftovers are not scanned, so pre-existing CRLF files
# in the tree do not mask a real violation in the change being committed.
if ($Files -eq "") {
    $list = @(git diff --cached --name-only 2>$null)
} else {
    $list = $Files.Split(",")
}

$exts = @(".go", ".json", ".sh", ".yml", ".yaml", ".md", ".service")
$fail = 0
$checked = 0

foreach ($f in ($list | Where-Object { $_ -ne "" } | Sort-Object -Unique)) {
    if ($exts -notcontains [System.IO.Path]::GetExtension($f)) { continue }
    if (-not (Test-Path -LiteralPath $f)) { continue }
    $checked++
    $bytes = [System.IO.File]::ReadAllBytes($f)
    if ($bytes.Length -ge 3 -and $bytes[0] -eq 239 -and $bytes[1] -eq 187 -and $bytes[2] -eq 191) {
        Write-Output "BOM : $f"
        $fail = 1
    }
    $text = [System.Text.Encoding]::UTF8.GetString($bytes)
    if ($text.Contains("`r`n")) {
        Write-Output "CRLF: $f"
        $fail = 1
    }
}

Write-Output "checked: $checked files"
if ($fail -ne 0) {
    Write-Output "RESULT: FAIL"
    exit 1
}
Write-Output "RESULT: PASS"
exit 0
$ErrorActionPreference = 'Stop'
Push-Location (Join-Path $PSScriptRoot '..')
try {
    rtk proxy python scripts/demo.py @args
    if ($LASTEXITCODE -ne 0) { throw 'API demo failed. Inspect the assertion above.' }
} finally {
    Pop-Location
}

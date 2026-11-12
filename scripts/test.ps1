param([string]$DatabaseUrl = 'postgres://peruri:peruri_local@127.0.0.1:55432/peruri?sslmode=disable')
$ErrorActionPreference = 'Stop'
$previous = $env:TEST_DATABASE_URL
Push-Location (Join-Path $PSScriptRoot '..')
try {
    $env:TEST_DATABASE_URL = $DatabaseUrl
    rtk proxy go build ./...
    if ($LASTEXITCODE -ne 0) { throw 'Build failed' }
    rtk proxy go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'Vet failed' }
    rtk proxy go test -race -count=10 ./...
    if ($LASTEXITCODE -ne 0) { throw 'Race/integration tests failed' }
    rtk proxy go test ./test -run '^$' -bench BenchmarkProcessBatch -benchmem
    if ($LASTEXITCODE -ne 0) { throw 'Benchmark failed' }
} finally {
    $env:TEST_DATABASE_URL = $previous
    Pop-Location
}

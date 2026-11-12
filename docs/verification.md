# Bukti verifikasi

Tanggal: 12 November 2026. Lingkungan lokal Windows amd64, Go 1.26.11, PostgreSQL 17 melalui Docker Desktop. Pengujian database memakai schema terisolasi; data demo pengguna tidak dihapus.

## Pemeriksaan sebelum publikasi

| Pemeriksaan | Hasil yang diamati |
|---|---|
| `rtk proxy powershell -NoProfile -File scripts/test.ps1` | Build, vet, race test 10 kali, dan benchmark lulus; suite race 42,482 detik |
| Benchmark PostgreSQL, create dan submit 20 dokumen | 40 iterasi, 28.854.395 ns/op, 94.446 B/op, 1.277 allocs/op; bukan benchmark Principal perusahaan |
| Checkout bersih dari index Git | `go build ./...` dan `go test -race ./...` lulus; suite race 6,201 detik |
| Bootstrap database kosong | API memasang `db/schema.sql` langsung pada schema PostgreSQL baru, kemudian health check 200 |
| Startup native dari dotenv | API dan simulator membaca file yang berasal dari `.env.example`; hanya alamat QA serta search_path database diubah untuk isolasi |
| Demo HTTP PostgreSQL | Keenam skenario di `scripts/demo.py` lulus, termasuk callback paralel, retry, batas global tiga request, dan timeout |
| Postman melalui Newman | 16 langkah, 18 HTTP request termasuk pembacaan jadwal retry, 23 assertion, nol kegagalan |
| Input yang tidak didukung PostgreSQL | HTTP dengan payload NUL menghasilkan 400; create dan callback invalid tidak meninggalkan data parsial |
| Urutan dokumen | Respons create dan list tetap memiliki urutan ID numerik yang sama, termasuk melewati ID 9 -> 10 |
| OpenAPI dan Swagger | Tes kesesuaian sembilan route, security, reference schema, halaman UI dan aset CSS/JS lokal lulus; lint Redocly 2.77.0 lulus pada verifikasi sebelumnya |
| Dependency | `rtk proxy go run golang.org/x/vuln/cmd/govulncheck@latest ./...`: No vulnerabilities found |
| Build image Linux | `rtk proxy docker --context desktop-linux compose build`: image API dan Principal terbaru berhasil dikompilasi; startup container tidak diulang pada pemeriksaan ini |
| Kebersihan source | gofmt bersih, `git diff --cached --check` lulus, tautan lokal README/docs ter-resolve |
| File publik | `.env`, PDF soal, binary, dan artifacts diabaikan Git. Pemeriksaan signature token/private key dan perbandingan nilai `.env` lokal tidak menemukan kebocoran; kredensial contoh memang khusus demo lokal |

Bukti mentah disimpan lokal di `artifacts/submission-runtime.json`, `artifacts/submission-postman.json`, log API/Principal, dan `artifacts/staged-audit.json`. Folder artifacts tidak dipublikasikan. Source dan perintah di atas cukup untuk mengulangi pengujian.

## Skenario yang dilindungi tes regresi

| Perilaku | Test di test/ |
|---|---|
| Maksimum tiga request lintas batch | TestGlobalConcurrencyAcrossBatches |
| Maksimum tiga attempt dan satu claim per attempt | TestRetryStopsAtThreeAttempts; TestOneClaimPerAttempt |
| Callback paralel idempotent, SUCCESS terminal | TestCallbacksAreAtomicAndSuccessTerminal |
| Callback attempt lama/future dan collision ID | TestOldCallbackCannotFailNewAttempt; TestEarlierSuccessResolvesUncertainRetriedRequest; TestRepositoryRejectsInvalidAndConflictingCallbacks |
| Cancellation, timeout, callback sebelum Submit selesai | TestCancelJoinsWorkersAndLeavesNoUnrecoverableClaims; TestTimeoutAndCallbackBeforeSubmitReturn |
| Operasi campuran dengan race detector | TestMixedConcurrentOperations |
| Backoff, jitter, dan exact deadline | TestExponentialRetryPolicy; TestRetryBackoffBlocksEarlyRequeue |
| Duplikat tidak menggeser deadline dan retry paralel hanya sekali | TestCallbackBackoffPreservesDeadlineAndAttemptLimit |
| Lease recovery, jitter per dokumen, chunk dan SKIP LOCKED | TestRecoverySchedulesIndependentJitterAndSuccessClearsIt; TestRecoveryBackoffIsBoundedAndSkipsLockedRows |
| Skema langsung, constraint jadwal, dan rollback DDL | TestSchemaDefinesRetrySchedule; TestSchemaInitializationRollsBackOnFailure |
| Rollback create serta delapan jawaban query SQL | TestPostgresCreateRollbackAndRecovery; TestSubmittedQueries |
| Wiring DI, autentikasi, dan validasi HTTP | TestAPIValidationAuthAndWorkflow; TestHandlerAcceptsApplicationInterfaceAndPreservesContext; TestRequestValidationRejectsBeforeService; TestRequestValidationBoundaries |
| Korelasi request ID dan alasan konflik | TestAPIRequestsEmitCorrelatedLogs; TestCallbackConflictLogsAndResponseShareRequestID; TestCallbackConflictsExplainDocumentState |
| Parsing MIME, trailing JSON dan batas body | TestJSONRequestBoundaries |
| Urutan ID numerik dan penolakan NUL PostgreSQL | TestPostgresListsDocumentsInCreationOrder; TestPostgresRejectsUnsupportedTextAsInvalidInput |
| Konfigurasi callback sesuai listener demo | TestDemoConfigurationMatchesListeners; TestLoadEnv |
| Dokumentasi dan susunan file | TestOpenAPIDocumentation; TestSourceLayout |

Semua 14 file test berada di test/. Satu fixture membuat schema PostgreSQL unik, memasang skema, dan membersihkan hanya schema tersebut. TEST_DATABASE_URL wajib; tidak ada repository memory atau test database yang dilewati diam-diam.

## Batas bukti

Tidak ada jaminan seluruh edge case sudah tertutup. Bukti ini mencakup skenario di atas pada lingkungan yang diuji. Principal perusahaan, reconciliation API, production load, dan deployment production belum diuji. Simulator callback bukan penyimpanan antrean yang durable. Efek logis tunggal tetap bergantung pada idempotency Principal; batas tiga request berlaku dalam satu instance.

Retry tetap dipicu eksplisit lewat RetryFailed lalu ProcessBatch setelah next_retry_at jatuh tempo. Skema database existing tidak di-upgrade otomatis ketika schema.sql diubah. ORM dan metrics belum diimplementasikan; penyimpanan memakai pgx dan structured logging sudah tersedia.

Workflow GitHub Actions tersedia di `.github/workflows/test.yml` dengan PostgreSQL 17, build, vet, format check, serta race test 10 kali. Hasil hosted CI dilihat pada tab Actions untuk commit yang dikirim.

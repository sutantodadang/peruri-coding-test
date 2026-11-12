# Peruri — Bulk Document Processor

Implementasi Coding Test.pdf dalam Go dengan **PostgreSQL sebagai satu-satunya penyimpanan**, termasuk untuk pengujian persistensi dan service. Principal eksternal disimulasikan melalui HTTP. Keenam fungsi wajib berada di [Service.go](internal/apps/docproc/service/Service.go).

## Mulai demo

Untuk menjalankan API langsung dengan Go di Windows, siapkan konfigurasi dan PostgreSQL dari root project:

```powershell
rtk proxy cmd /c "if not exist .env copy .env.example .env"
rtk proxy docker --context desktop-linux compose stop api principal
rtk proxy docker --context desktop-linux compose up -d db --wait
```

Terminal pertama menjalankan simulator Principal:

```powershell
rtk proxy go run ./cmd/principal
```

Terminal kedua menjalankan API:

```powershell
rtk proxy go run ./cmd/main.go
```

Keduanya membaca `.env` dari working directory. Environment yang sudah disetel di terminal tetap diprioritaskan. File `.env.example` berisi URL PostgreSQL lokal, URL Principal/callback, dan token demo; salin sekali ke `.env`, yang tidak disimpan dalam Git maupun image Docker. File yang tidak ada diperbolehkan bila konfigurasi diberikan lewat environment. File yang rusak menghentikan startup dengan pesan tanpa isi kredensial.

Dengan .env.example, API Go tersedia di http://127.0.0.1:8000 dan Swagger di http://127.0.0.1:8000/docs/. Callback diarahkan ke port 8000 yang sama. Untuk Postman lokal, set baseUrl=http://127.0.0.1:8000; demo otomatis: `rtk proxy python scripts/demo.py --url http://127.0.0.1:8000`. Docker memakai port 8080 seperti contoh berikutnya.

API menerapkan migrasi versi secara default, sehingga database kosong dapat langsung digunakan. Gunakan `-migrate=false` bila migrasi dikelola terpisah. Jangan menjalankan API/Principal Docker dan proses Go lokal pada port yang sama.

Untuk menjalankan **seluruh layanan melalui Docker**, gunakan:

Perintah memakai prefix rtk proxy sesuai konvensi workspace. Jika RTK tidak terpasang, hilangkan prefix tersebut; build, test, dan aplikasi tidak bergantung pada RTK.

Prasyarat: Docker Desktop dengan Linux containers, Docker Compose, dan RTK sesuai instruksi workspace. Python 3 diperlukan untuk skrip demo otomatis. Go **1.26.11** dan compiler C diperlukan hanya untuk pengujian lokal dengan race detector. Go dengan GOTOOLCHAIN=auto akan mengunduh toolchain sesuai go.mod.

```powershell
rtk proxy docker --context desktop-linux compose up --build -d --wait
rtk proxy powershell -NoProfile -File scripts/demo.ps1
```

API: `http://127.0.0.1:8080`. Mock Principal: `http://127.0.0.1:9000`. PostgreSQL: `127.0.0.1:55432`, database/user `peruri`, password lokal `peruri_local`. Semua port host dibatasi loopback. Pada Linux atau Docker context lain, hilangkan `--context desktop-linux`.

Compose memasang `db/schema.sql` secara transaksional pada startup API pertama. Menjalankan ulang perintah tidak menghapus data. Project/volume bernama `peruri-bulk-demo` agar terpisah dari database lain. Token demo ada di [.env.example](.env.example); untuk override buat `.env` lalu sesuaikan token di Postman atau environment skrip. Kredensial contoh hanya untuk demo lokal.

Hasil yang diharapkan: enam langkah `PASS`, bukti JSON di `artifacts/demo.json`, dan record tetap tersimpan di PostgreSQL. Skrip memakai batch_no unik sehingga aman dijalankan ulang. Ia memeriksa:

1. Create dan proses batch dengan hasil campuran.
2. Callback SUCCESS serta 24 callback duplikat paralel: satu record event.
3. Late FAILED tetap mempertahankan SUCCESS dan collision event menghasilkan 409.
4. Retry tepat tiga attempt maksimum; SUCCESS tidak diproses ulang.
5. Dua batch bersamaan, total 24 dokumen: max_inflight mock = 3.
6. Timeout Submit: HTTP 504 dan dokumen FAILED.

## OpenAPI dan Swagger UI

Setelah API berjalan, buka **[http://127.0.0.1:8080/docs/](http://127.0.0.1:8080/docs/)**. Restart proses Go bila masih menjalankan binary sebelum perubahan ini. Spesifikasi OpenAPI 3.0.3 tersedia di [http://127.0.0.1:8080/openapi.json](http://127.0.0.1:8080/openapi.json), dengan source tunggal [docs/openapi.json](docs/openapi.json). Aset Swagger UI dan spesifikasi disertakan dalam binary, sehingga tidak membutuhkan CDN saat demo. Server API mengikuti origin halaman, termasuk bila port diubah.

Klik **Authorize**, isi `ApiToken` dengan nilai API_TOKEN dan `CallbackToken` dengan nilai CALLBACK_TOKEN dari `.env`, tanpa awalan `Bearer`. Default lokalnya `local-api-token` dan `local-callback-token`. Token tidak diisi otomatis dan tidak disimpan setelah reload halaman. Dokumentasi dapat dibuka tanpa token; operasi bisnis tetap memerlukan token yang sesuai.

Alur **Try it out** untuk demo:

1. **CreateBatch**: pilih contoh `manual`, ganti batch_no agar unik, lalu Execute. Simpan ID dokumen dari respons 201.
2. **ProcessBatch**: isi parameter `no` dengan batch_no tadi. Respons 200 berarti Submit selesai; dokumen manual tetap PROCESSING.
3. **GetBatchStatus** / **ListDocuments**: lihat progres, ID, dan attempt_count aktual.
4. **HandleCallback**: isi document_id aktual, attempt_count=1, callback_id unik, status=SUCCESS. Callback memakai CallbackToken.
5. **GetBatchStatus**: status menjadi COMPLETED. Kirim ulang callback yang sama untuk membuktikan idempotency; periksa **ListCallbacks** untuk auditnya.

Contoh `mixed` menambahkan satu penolakan Principal; ProcessBatch diharapkan mengembalikan 502. Baca `next_retry_at` pada ListDocuments, tunggu hingga jatuh tempo, lakukan RetryFailed, lalu ProcessBatch untuk menguji batas tiga attempt.

Request create/callback divalidasi memakai `go-playground/validator/v10` sebelum service dipanggil. Respons 400 menyebut field JSON dan aturan yang gagal, misalnya:

```json
{"error":"validation failed","fields":[{"field":"documents[0].document_no","rule":"required"}]}
```

Batas nama/message dihitung dalam byte UTF-8 sesuai kontrak service. Dokumen harus berjumlah 1..1000 dan document_no unik; payload harus JSON non-null. Callback hanya menerima SUCCESS/FAILED, attempt 1..3, dan ID desimal positif kanonik. Service tetap menjaga invariant untuk pemanggil non-HTTP.

Lint spesifikasi: `rtk proxy npx.cmd --yes @redocly/cli@2.77.0 lint docs/openapi.json --extends minimal`. Test Go memeriksa kesesuaian seluruh route, reference schema, autentikasi dalam spec, serta ketersediaan aset UI lokal.

## Demo manual dengan Postman

Import [postman/peruri.postman_collection.json](postman/peruri.postman_collection.json). Jalankan Collection Runner berurutan dari 01 sampai 16. Batch_no, callback_id, dan document_id diisi otomatis. Token default sudah cocok dengan Compose. **HTTP 502 pada request 02, 10, dan 12 adalah hasil yang diharapkan**, karena payload sengaja meminta penolakan Principal. Assertion Postman memeriksanya. Request retry 09 dan 11 membaca `next_retry_at` dan menunggu jadwal tersebut melalui pre-request script; tidak perlu mengatur delay Collection Runner.

Koleksi sudah dijalankan melalui Newman: 16 langkah, 18 HTTP request (termasuk dua pembacaan jadwal retry), dan 23 assertion lulus. Koleksi dan requests.http berada di folder postman/. Jika Node.js tersedia, jalankan dari terminal Windows dengan `rtk proxy npx.cmd --yes newman run postman/peruri.postman_collection.json`. Pada Linux gunakan npx tanpa akhiran .cmd. Newman hanya alat verifikasi, bukan dependency aplikasi Go.

Untuk presentasi sekitar lima menit:

- Jalankan request 01–03: tunjukkan PENDING -> PROCESSING/FAILED dan attempt_count.
- Jalankan 04–08: kirim SUCCESS, kirim duplikat, lalu FAILED terlambat; status tetap SUCCESS dan audit hanya dua event unik.
- Jalankan 09–16: retry dua kali, buktikan attempt keempat tidak terjadi. Jelaskan bahwa COMPLETED berarti tidak ada pekerjaan tersisa, bukan seluruh dokumen sukses.
- Jalankan skrip demo untuk menunjukkan batas concurrency lintas batch dan timeout.
- Buka [db/queries.sql](db/queries.sql) untuk menjelaskan SKIP LOCKED dan transaksi callback.

Alternatif tanpa Postman: [postman/requests.http](postman/requests.http). Health check dari terminal:

```powershell
rtk proxy curl.exe http://127.0.0.1:8080/healthz
```

Gunakan batch_no baru untuk create berikutnya; duplikat menghasilkan 409.

## API

Endpoint bisnis memerlukan `Authorization: Bearer <token>`. Callback memakai token terpisah. Health, `/docs/`, dan `/openapi.json` bersifat publik. Body JSON maksimum 1 MiB, maksimum 1000 dokumen per batch, unknown fields ditolak, dan maksimum 32 request API aktif; sisanya 429. Request timeout API 60 detik. Batas Principal tetap 3 walaupun banyak pemanggilan API berjalan.

| Method dan path | Fungsi / hasil |
|---|---|
| GET /docs/ | Swagger UI interaktif dengan sembilan operasi API |
| GET /openapi.json | Spesifikasi OpenAPI 3.0.3 |
| GET /healthz | Koneksi PostgreSQL: 200 atau 503 |
| POST /batches | CreateBatch: 201, duplicate batch_no 409 |
| POST /batches/{batch_no}/process | ProcessBatch: tunggu seluruh Submit, 200/502/504 |
| POST /documents/{id}/process | ProcessDocument; non-PENDING dilewati tanpa error |
| POST /callbacks | HandleCallback: baru/duplikat sah 200, collision/future attempt 409 |
| POST /batches/{batch_no}/retry | RetryFailed: `{ "retried": n }`, **hanya requeue** dokumen yang sudah jatuh tempo |
| GET /batches/{batch_no} | GetBatchStatus: counts semua status, status batch, done |
| GET /batches/{batch_no}/documents | Dokumen, status, attempt, request_id, last_error, next_retry_at |
| GET /documents/{id}/callbacks | Audit callback unik, urut ID |

Create request hanya menerima batch_no, document_no, payload. ID/status/attempt/request_id dibuat server. Contoh callback:

```json
{"callback_id":"principal-event-001","document_id":"1","attempt_count":1,"status":"SUCCESS","message":""}
```

Gunakan ID aktual dari respons create; ID PostgreSQL dikirim sebagai string desimal kanonik. Callback status hanya SUCCESS/FAILED. attempt_count harus 1..3 dan menunjuk attempt yang sudah dikirim. FAILED lama dicatat tanpa mengubah attempt baru; SUCCESS lama boleh menyelesaikan PROCESSING/FAILED karena request_id stabil. SUCCESS ketika dokumen PENDING ditolak 409 tanpa record agar Principal dapat mengirim ulang setelah claim. Duplikat ID dengan isi berbeda menghasilkan 409.

ProcessBatch menyelesaikan fase **pengiriman**, bukan menunggu semua callback. Kegagalan satu Submit tidak menghentikan dokumen lainnya. Respons 502/504 bukan rollback batch: periksa dokumen untuk mengetahui hasil parsial. RetryFailed harus diikuti ProcessBatch secara eksplisit.

Retry memakai **exponential backoff dengan equal jitter**. Sesudah kegagalan attempt 1, tunggu 1–2 detik; sesudah attempt 2, tunggu 2–4 detik (batas atas eksklusif). Attempt 3 tidak dijadwalkan lagi. Rumusnya `window = 1s * 2^attempt`, `delay = window/2 + random[0, window/2)`. Kegagalan Submit, callback FAILED, dan lease timeout memakai policy yang sama, diinjeksi ke kedua repository secara terpisah.

`next_retry_at` disimpan di PostgreSQL dan terlihat pada ListDocuments; field dihilangkan bila tidak ada jadwal. Retry sebelum waktunya langsung mengembalikan `200 {"retried":0}`. Setelah jatuh tempo, panggil RetryFailed lalu ProcessBatch. Menunggu backoff tidak menahan worker atau slot Principal. Pemanggilan retry paralel hanya dapat me-requeue satu kali; callback duplikat/FAILED terlambat tidak menggeser jadwal. SUCCESS menghapus jadwal retry. Trigger pengiriman tetap eksplisit untuk demo/API.

Kolom `next_retry_at`, CHECK konsistensi jadwal, dan partial index antrean retry didefinisikan langsung dalam `db/schema.sql`. Tidak ada file migrasi tambahan untuk retry. Startup memasang seluruh skema pada database kosong dan tidak mengubah database yang sudah diinisialisasi. Untuk bootstrap dari skema lama, gunakan database demo baru; database existing harus sudah sesuai dengan definisi skema terbaru. Jika skema dipasang manual, jalankan `db/schema.sql` sebelum memakai `-migrate=false`.

Payload mode khusus simulator:

| mode | Perilaku mock Principal |
|---|---|
| manual | Terima 202, tanpa callback otomatis |
| success / tidak diisi | Terima 202 dan kirim SUCCESS asynchronous |
| failed | Terima 202 dan kirim FAILED asynchronous |
| reject | Tolak Submit dengan 503, dokumen menjadi FAILED |
| fail_once | Tolak attempt 1, attempt berikutnya kirim SUCCESS |
| timeout | Lebih lama daripada timeout Submit 5 detik |

`GET http://127.0.0.1:9000/stats` dengan Bearer `local-principal-token` menunjukkan inflight, max_inflight, dan total_requests mock sejak startup. Simulator hanya untuk lokal dan tidak menggantikan kontrak Principal perusahaan.

## Pengujian

```powershell
rtk proxy powershell -NoProfile -File scripts/test.ps1
```

Skrip menjalankan build, vet, **go test -race -count=10 ./... dengan PostgreSQL**, serta benchmark. Semua file pengujian berada di [test/](test/). Satu fixture [postgres_test.go](test/postgres_test.go) membuat schema unik per test, menjalankan migrasi, lalu menghapus hanya schema uji tersebut. Tidak menghapus data demo. Role test memerlukan CREATE SCHEMA; pakai database/role test tersendiri di lingkungan bersama.

Perintah terpisah (set TEST_DATABASE_URL terlebih dahulu untuk test dan benchmark):

```powershell
rtk proxy go build ./...
rtk proxy go vet ./...
rtk proxy go test -race -count=10 ./...
rtk proxy go test ./test -run '^$' -bench BenchmarkProcessBatch -benchmem
rtk proxy go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

**TEST_DATABASE_URL wajib untuk suite lengkap.** Test database gagal dengan pesan konfigurasi yang jelas bila variabel tidak tersedia. Gunakan scripts/test.ps1 untuk mengisinya otomatis. Workflow [.github/workflows/test.yml](.github/workflows/test.yml) menyiapkan PostgreSQL 17 dan variabel tersebut; hosted CI belum dijalankan dari workspace ini.

Test concurrency memakai barrier/channel dan WaitGroup, bukan time.Sleep. Test maksimum concurrency menahan Submit sampai tiga request benar-benar masuk; 24 dokumen diproses dari dua batch. Cancellation menunggu semua worker selesai dan memeriksa inflight=0, tidak ada PROCESSING tertinggal, serta dokumen belum disentuh tetap PENDING. Timeout test menunggu ctx.Done, tidak mengandalkan urutan scheduling. Timer 10 detik hanya pengaman deadlock. Benchmark mengukur create dan pengiriman 20 dokumen melalui repository PostgreSQL dengan mock Principal langsung; tidak mengukur latency Principal perusahaan. TestSourceLayout memeriksa lokasi test dan salinan source yang identik.

Lihat [docs/verification.md](docs/verification.md) untuk hasil aktual dan [docs/answers.md](docs/answers.md) untuk jawaban esai A–D.

## Desain dan recovery

```text
container.SetupContainer
  NewRetryPolicy(time.Now, rand.Float64) -> retry
  batch.NewBatchRepository(pool, retry) -> IBatchRepository
  callback.NewCallbackRepository(pool, retry) -> ICallbackRepository
  middlewares.NewMiddleware(tokens, logger)
  NewDocProcService(batchRepo, callbackRepo, principalClient, timeout) -> IDocProcService
  NewRequestValidator() -> validator.Validate
  NewDocProcHandler(docProcService, validate) -> IDocProcHandler
  route.RegisterDocProcRoute(app, docProcHandler, middleware)
  route.RegisterDocumentationRoute(app)
                     |
                     +-> Principal HTTP -> callback HTTP
```

Service memiliki satu semaphore kapasitas 3. Slot diperoleh sebelum claim. Claim PENDING -> PROCESSING sekaligus menaikkan attempt dalam statement atomik SKIP LOCKED; tidak ada lock selama Submit. Worker per ProcessBatch dibatasi 3 dan semuanya di-join. Cleanup setelah cancellation mendapat context terpisah maksimal 3 detik untuk menyimpan FAILED. Bila database tetap gagal, lease recovery menangani PROCESSING yang tertinggal.

Struktur mengikuti pola container yang diberikan:

| Pemilik | Kontrak / implementasi |
|---|---|
| Batch repository | [IBatchRepository](internal/repositories/batch/IBatchRepository.go); create, list, claim, submit failure, retry, recovery |
| Callback repository | [ICallbackRepository](internal/repositories/callback/ICallbackRepository.go); transaksi callback dan audit event |
| Service | [IDocProcService](internal/apps/docproc/service/IService.go); menerima kedua interface repository dan IPrincipalClient secara langsung |
| Handler | [IDocProcHandler](internal/http/handler/IHandler.go); NewDocProcHandler menerima IDocProcService dan instance validator yang sudah dikonfigurasi |
| Validasi request | [validation.go](internal/http/handler/validation.go); DTO create, tag validator, nama field JSON, error field/rule; ID kanonik dimiliki domain docproc |
| Middleware | internal/http/middlewares; token, deadline, security header, dan satu admission limit untuk seluruh route |
| Route | [RegisterDocProcRoute](internal/http/route/DocProcRoute.go); path dan method terpisah dari constructor handler |
| Dokumentasi API | [docs/](docs/); openapi.json dan Swagger UI di swagger.go |
| Principal | [IPrincipalClient](internal/apps/principal/IPrincipalClient.go); adapter HTTP di package yang sama menerima HTTPClient yang memiliki Do |
| PostgreSQL adapter | internal/repositories/batch dan internal/repositories/callback; claim atomik, insert callback + update dokumen dalam satu transaksi |
| Database | db/; pool, migrasi, dan SQL canonical sesuai layout terbaru |
| Container | [SetupContainer](internal/container/Container.go); merakit repository -> middleware -> service -> handler -> route, dan dapat diuji langsung dari test/ |
| Lifecycle | [cmd/main.go](cmd/main.go); mengelola pool, server, worker, dan cleanup container |

Wiring eksplisit tanpa DI container:

```go
retry := docproc.NewRetryPolicy(time.Now, rand.Float64) // math/rand/v2
batchRepo := batch.NewBatchRepository(pool, retry)
callbackRepo := callback.NewCallbackRepository(pool, retry)
middleware := middlewares.NewMiddleware(config.APIToken, config.CallbackToken, slog.Default())
docProcService, err := service.NewDocProcService(batchRepo, callbackRepo, principalClient, 5*time.Second)
if err != nil {
    return err
}
validate, err := handler.NewRequestValidator()
if err != nil {
    return err
}
docProcHandler := handler.NewDocProcHandler(docProcService, validate)
route.RegisterDocProcRoute(app, docProcHandler, middleware)
route.RegisterHealthRoute(app, pool, middleware)
route.RegisterDocumentationRoute(app)
```

Kedua repository diinjeksi terpisah, tanpa aggregate atau service locator. Service memakai kontrak, tidak membuat repository atau adapter HTTP sendiri. IPrincipalClient didefinisikan di package Principal; service docproc mengimpor kontrak tersebut. Package Principal tidak mengimpor service docproc.

Timeout service diberikan melalui constructor dan tidak dapat dimutasi setelah konstruksi. Constructor menolak masing-masing dependency nil dan timeout yang tidak valid terhadap lease. Read endpoint dan recovery juga melalui service. Kedua repository memiliki pemeriksaan implementasi interface saat kompilasi. Test container menjalankan create -> process -> callback dengan PostgreSQL dan Principal HTTP lokal; test middleware membuktikan admission limit dibagi lintas route.

Recovery berjalan saat startup dan setiap 10 detik, mengubah maksimal 100 dokumen PROCESSING per putaran yang lebih tua dari 60 detik menjadi FAILED dan menjadwalkan backoff bila attempt tersisa. Ia tidak mengirim ulang otomatis dan tidak mereset attempt. Submit timeout 5 detik lebih pendek dari lease. Callback insert + update berada dalam satu transaksi dengan document row lock dan UNIQUE callback_id. Status batch diturunkan dari dokumen agar tidak basi. Tidak ada global mutable state untuk semaphore/repository.

pgx dipilih karena query locking/transaksi menjadi eksplisit; ORM bersifat nilai tambah opsional dalam soal. Seluruh skenario persistensi diuji langsung pada PostgreSQL. Fake dependency hanya dipakai untuk batas aplikasi/HTTP dan Principal.

## Operasi lokal

Log API ditulis sebagai JSON ke terminal/stdout. Setiap request bisnis dan health mendapat `X-Request-ID` baru; ringkasannya mencatat request_id, method, route, status, response_bytes dan duration_ms. Level INFO untuk keberhasilan, WARN untuk 4xx, dan ERROR untuk 5xx atau request yang terputus karena panic. Status 0 pada log aborted berarti belum ada final response yang dikirim. Header request ID dari client tidak dipakai ulang. Token Authorization, body, query string dan isi message callback tidak dicatat.

Callback juga mencatat callback_id, document_id, attempt_count dan callback_status. Penolakan menambahkan reason, document_status dan current_attempt_count. Gunakan request_id yang sama dari respons/header untuk mencari kedua log. ID HTTP ini berbeda dari request_id stabil milik dokumen yang digunakan sebagai idempotency key Principal.

Contoh alasan 409:

- `callback_requires_processing`: dokumen masih PENDING. Sesudah RetryFailed, panggil ProcessBatch atau ProcessDocument lagi sebelum mengirim SUCCESS.
- `callback_future_attempt`: attempt_count callback melebihi attempt yang tercatat. Periksa ListDocuments terlebih dahulu.
- `callback_id_reused`: ID event sudah tersimpan dengan isi berbeda, termasuk perubahan message. `conflicting_fields` hanya menampilkan nama field; replay harus identik atau memakai ID baru untuk event baru.

Respons 409 callback menyertakan code, message petunjuk, document_status, current_attempt_count, dan request_id. Tetap menghasilkan ErrConflict pada pemanggil Go. Menambah log tidak mengubah aturan state, retry, maupun idempotency.

```powershell
rtk proxy docker --context desktop-linux compose ps
rtk proxy docker --context desktop-linux compose logs api principal --tail 50
rtk proxy docker --context desktop-linux compose exec -T db psql -U peruri -d peruri -c 'SELECT id,status,attempt_count,request_id FROM documents ORDER BY id;'
rtk proxy docker --context desktop-linux compose stop
rtk proxy docker --context desktop-linux compose start
```

Stop/start mempertahankan volume. Jangan menghapus volume jika masih membutuhkan data demo. Jika port bentrok, ubah port host di compose.yaml dan baseUrl Postman/demo. Bila startup database bermasalah, lihat logs db; jangan mengosongkan volume lama secara otomatis.

Untuk proses Go lokal, ikuti langkah dua terminal di awal README. API memakai HTTP_ADDR (default 127.0.0.1:8080); simulator memakai HTTP_ADDR_PRINCIPAL (default 127.0.0.1:9000). Samakan PRINCIPAL_URL/CALLBACK_URL dengan alamat yang dipilih. Compose memakai nama variabel yang sama dengan executable masing-masing.

## Batas dan perbaikan berikutnya

Belum terhubung ke Principal perusahaan, belum deployment production, dan belum ada polling reconciliation API Principal. Jaminan pengiriman realistis at-least-once; efek logis tunggal bergantung pada dukungan idempotency key Principal. Batas tiga request berlaku per Service/instance sesuai soal, bukan global multi-replica.

Tambahan yang masuk akal jika waktu tersedia: status reconciliation sebelum retry hasil ambigu, durable callback delivery di mock/Principal, observability metrik, dan pengukuran EXPLAIN BUFFERS pada volume representatif. Auth saat ini token service tunggal; tenant/user authorization memerlukan kontrak produk tambahan. Tidak ada UI tambahan karena demo difokuskan pada API.

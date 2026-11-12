# Jawaban transaksi dan performa

## A. SELECT lalu UPDATE tanpa locking

Worker A membaca dokumen 42 PENDING. Worker B membaca status yang sama sebelum A mengubahnya. Keduanya mengirim dokumen ke Principal, lalu keduanya menulis PROCESSING. Status akhir terlihat benar tetapi Principal menerima dua pengiriman. Membungkus SELECT dan UPDATE dalam transaksi READ COMMITTED biasa masih memungkinkan race ini.

Mitigasi pertama: `SELECT ... FOR UPDATE SKIP LOCKED`, kemudian UPDATE claim + increment attempt dalam transaksi yang sama. Worker lain melewati row yang terkunci. Commit dilakukan sebelum Submit supaya koneksi jaringan tidak menahan row lock. Query 5 menggunakan CTE sehingga claim adalah satu statement atomik. Repository production memakai bentuk yang sama untuk satu ID, sesuai kontrak ProcessDocument.

Mitigasi kedua: compare-and-swap `UPDATE documents SET status='PROCESSING', attempt_count=attempt_count+1 WHERE id=$1 AND status='PENDING' AND attempt_count<max_attempts RETURNING *`. Hanya worker yang menerima row boleh Submit. Zero row berarti tidak memperoleh claim. SERIALIZABLE juga dapat digunakan, tetapi perlu menangani serialization failure dan tidak boleh mengulang side effect eksternal sembarangan.

## B. Transaction, row lock, unique constraint

Transaction memastikan create batch dan seluruh dokumennya commit bersama. Jika dokumen kedua melanggar UNIQUE(batch_id,document_no), batch dan dokumen pertama ikut rollback. Transaction sendiri tidak mencegah dua pembaca mengamati PENDING bersamaan.

Row lock mengatur siapa yang boleh mengubah dokumen 42 saat ini. Callback mengunci row dokumen sebelum memeriksa attempt, mencatat callback, dan memperbarui status. Lock tidak menyimpan ingatan event setelah transaksi selesai dan tidak mencegah dua insert berbeda dengan callback_id sama.

Unique constraint menyimpan aturan lintas transaksi: batch_no unik, document_no unik di dalam batch, request_id unik, dan callback_id unik global. `ON CONFLICT DO NOTHING RETURNING id` membedakan event baru dari duplikat. Unique constraint sendiri tidak membuat insert event dan update status menjadi atomik.

Ketiganya dipakai bersama: BEGIN -> lock document -> validasi attempt -> INSERT callback ON CONFLICT -> jika baru, UPDATE bersyarat -> COMMIT. Jika duplikat, baca record existing dan bandingkan semua field; isi berbeda menjadi conflict, bukan dianggap pengulangan sah. FAILURE tidak dapat menimpa SUCCESS. SUCCESS dari attempt yang sudah pernah dikirim boleh memperbaiki PROCESSING/FAILED meskipun sudah retry, karena Principal dapat mengembalikan hasil request_id asli. FAILED attempt lama disimpan untuk audit tanpa mengubah attempt baru. Callback future attempt ditolak tanpa record supaya Principal dapat mengirim ulang setelah claim yang benar.

## C. Request diterima tetapi update database gagal

Claim PROCESSING dan request_id sudah commit sebelum jaringan dipanggil. Semua retry mengirim `Idempotency-Key` yang stabil per dokumen, sementara attempt_count mengidentifikasi percobaan lokal dan memagari callback lama. Principal nyata harus menyimpan key dan hasil logis secara persisten. Key baru untuk setiap retry tidak cukup untuk mencegah pekerjaan logis ganda.

Jika Principal menerima request lalu koneksi putus, server tidak tahu apakah pekerjaan sudah diterima. Jika penulisan FAILED juga gagal, row masih PROCESSING. Respons error dan log tidak boleh disamakan dengan bukti bahwa Principal menolak dokumen. Callback SUCCESS dari attempt yang sudah dikirim tetap dapat memperbaiki PROCESSING/FAILED, termasuk bila hasil asli datang setelah retry.

Implementasi ini menyimpan locked_at saat claim. Recovery setiap 10 detik mengubah PROCESSING yang lebih tua dari 60 detik menjadi FAILED tanpa menambah attempt dan tanpa mengirim ulang otomatis. Submit timeout 5 detik dan cleanup timeout 3 detik lebih pendek dari lease, sehingga recovery normal tidak mengambil alih Submit yang masih berjalan. Retry eksplisit tetap tunduk pada maksimum tiga attempt. Jika tidak ada callback setelah attempt ketiga, lease recovery membuat batch COMPLETED dengan FAILED; operator harus melakukan investigasi, bukan reset counter.

Untuk integrasi nyata, lakukan reconciliation dengan API status Principal berdasarkan request_id sebelum replay pekerjaan yang hasilnya tidak pasti. Jika job masih berjalan, perpanjang lease atau tunggu callback. Jika sudah selesai, terapkan hasil melalui jalur callback idempotent. Implementasi demo belum memiliki endpoint polling status dari Principal perusahaan. SUCCESS saat dokumen kembali PENDING ditolak 409 tanpa mencatat event karena state machine PDF hanya mengizinkan PROCESSING/FAILED -> SUCCESS. Principal dapat mengirim ulang event yang sama setelah claim. Rekonsiliasi tetap diperlukan bila Principal tidak mengirim ulang. Fencing menahan FAILED lama agar tidak membatalkan attempt terbaru, tetapi membolehkan SUCCESS dari request asli.

Outbox relevan bila create batch dan publikasi job ke broker harus atomik: insert dokumen serta outbox dalam transaksi yang sama, dispatcher mengirim lalu menandai outbox. Crash setelah kirim tetapi sebelum menandai outbox tetap dapat mengirim ulang, sehingga idempotency Principal masih wajib. Tidak ada transaksi PostgreSQL yang mengikat HTTP eksternal menjadi exactly-once.

Jaminan: maksimal tiga attempt aplikasi dan claim tunggal per attempt, dengan pengiriman at-least-once bila operator melakukan retry. Efek logis tunggal di Principal bergantung pada dedup Principal. Tidak ada klaim exactly-once end-to-end. Semaphore berlaku untuk satu Service dalam satu instance; bila deployment diperbanyak, batas lintas instance memerlukan koordinasi tambahan. Repository PostgreSQL tetap mencegah claim ganda lintas instance.

## D. EXPLAIN ANALYZE

Contoh aman untuk read query pada database uji:

```sql
EXPLAIN (ANALYZE, BUFFERS)
SELECT id,document_no,status FROM documents
WHERE batch_id=42 AND status='PENDING' ORDER BY id LIMIT 3;
```

Nilai 42 di sini hanya contoh investigasi manual, sedangkan query aplikasi menggunakan parameter. Seq Scan membaca tabel; ini wajar pada tabel kecil atau filter yang memilih sebagian besar row. Index Scan mengambil kandidat lewat index dan mengakses heap. Bitmap Heap Scan dapat lebih baik untuk banyak kandidat. Jangan menyimpulkan Seq Scan selalu buruk.

Baca `rows` estimasi dibanding `actual rows`. Estimasi 10 tetapi aktual 100.000 dapat membuat planner memilih nested loop yang mahal. `loops=1000` berarti node dijalankan 1000 kali; actual time/rows umumnya rata-rata per loop, sehingga jangan membaca angka itu sebagai total. `Rows Removed by Filter` besar memberi petunjuk banyak data dibaca lalu dibuang. `Buffers: shared hit` adalah buffer cache, `read` adalah blok yang harus dibaca; repeated warm-cache test bukan bukti performa disk dingin.

Jika statistik usang setelah bulk load, jalankan ANALYZE documents, lalu bandingkan estimasi. Jika filter batch/status membuang banyak row, uji index `(batch_id,status,id)` dan ukur buffer/time sebelum-sesudah. Jika SUCCESS mendominasi, partial index queue dapat lebih kecil, tetapi hanya berlaku bila predicate query mengimplikasikan predicate index. Parameter predicate yang generik dapat menghalangi pemakaian partial index. Hindari fungsi/cast pada kolom filter dan periksa query N+1. `Sort Method: external merge Disk` atau temp buffers menandakan spill: coba index yang cocok dengan ORDER BY, batasi kandidat lebih awal, dan pertimbangkan work_mem per session dengan anggaran memori concurrency.

`EXPLAIN ANALYZE` benar-benar menjalankan UPDATE/DELETE. Gunakan salinan database atau staging terlebih dahulu. BEGIN/ROLLBACK dapat membatalkan perubahan row lokal tetapi tetap menahan lock, membangkitkan WAL, mengonsumsi sequence, dan tidak selalu membatalkan side effect eksternal dari trigger. Di production, pakai EXPLAIN tanpa ANALYZE untuk pemeriksaan awal, statement_timeout/lock_timeout, dan prosedur operasi yang disetujui. Tidak ada benchmark production atau klaim p95/p99 dalam submission ini.

## Penyesuaian skema dan asumsi

- Status batch diturunkan dari dokumen, bukan disimpan sebagai kolom kedua yang dapat basi. total_documents ditulis atomik saat create; tidak ada API menambah/menghapus dokumen.
- request_id UUID persisten: mencegah key bentrok setelah restart/recreate database. locked_at: recovery lease. callback attempt_count: fencing. Message disimpan sebagai teks, payload dokumen JSONB.
- CHECK membatasi status, attempt 0..3, max_attempts=3, dan konsistensi locked_at. SUCCESS bersifat terminal melalui jalur repository bersyarat; role database dengan SQL bebas tetap dapat melewati aturan aplikasi.
- Duplicate batch_no menghasilkan ErrConflict/HTTP 409 yang jelas, sesuai pilihan kontrak PDF. Payload/status dari client tidak dapat mengubah field internal.
- Backoff memakai equal jitter: `window = 1s * 2^attempt`, delay acak pada `[window/2, window)`. Retry pertama 1–2 detik, kedua 2–4 detik; attempt ketiga terminal. `next_retry_at` persisten, hanya FAILED eligible yang memiliki jadwal. RetryFailed memakai `next_retry_at <= clock.Now()` tanpa tidur/menahan semaphore; pengiriman tetap eksplisit lewat ProcessBatch. Duplikat dan late FAILED tidak menunda ulang jadwal. Kolom, constraint konsistensi jadwal, dan partial index retry berada langsung di `db/schema.sql`.
- Menggunakan pgx dan SQL eksplisit, bukan ORM, agar operasi locking/transaksi dapat ditinjau langsung. ORM hanya nilai tambah opsional dalam soal.
- Mock Principal hanya simulator lokal. Ia mengirim callback dengan ID deterministik; dedup bisnis persisten di Principal eksternal belum dibuktikan oleh mock.

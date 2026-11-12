-- PostgreSQL. Nilai bisnis/input menggunakan parameter; literal status adalah enum domain.

-- 1. Semua dokumen untuk batch_no $1, urutan stabil.
-- name: ListBatchDocuments
SELECT b.id AS batch_id,b.batch_no,b.total_documents,b.created_at AS batch_created_at,
       d.id,d.document_no,d.payload,d.status,d.attempt_count,d.max_attempts,
       d.request_id,d.last_error,d.locked_at,d.next_retry_at
FROM batches b JOIN documents d ON d.batch_id=b.id
WHERE b.batch_no=$1
ORDER BY d.id;

-- 2. Satu baris per batch, termasuk empat status yang bernilai nol.
-- name: BatchCounts
SELECT b.id,b.batch_no,COUNT(d.id) AS total,
 COUNT(d.id) FILTER(WHERE d.status='PENDING') AS pending,
 COUNT(d.id) FILTER(WHERE d.status='PROCESSING') AS processing,
 COUNT(d.id) FILTER(WHERE d.status='SUCCESS') AS success,
 COUNT(d.id) FILTER(WHERE d.status='FAILED') AS failed
FROM batches b LEFT JOIN documents d ON d.batch_id=b.id
GROUP BY b.id,b.batch_no ORDER BY b.id;

-- 3. Unfinished juga mencakup FAILED yang masih boleh di-retry.
-- name: UnfinishedBatches
SELECT b.id,b.batch_no,b.created_at
FROM batches b
WHERE EXISTS(SELECT 1 FROM documents d WHERE d.batch_id=b.id
 AND (d.status IN ('PENDING','PROCESSING')
      OR (d.status='FAILED' AND d.attempt_count<d.max_attempts)))
ORDER BY b.id;

-- 4. Retry eligible pada batch $1, waktu cutoff UTC $2 (clock aplikasi).
-- name: RetryableDocuments
SELECT d.* FROM documents d JOIN batches b ON b.id=d.batch_id
WHERE b.batch_no=$1 AND d.status='FAILED' AND d.attempt_count<d.max_attempts
 AND d.next_retry_at<=$2
ORDER BY d.id;

-- 5. $1 batch_no, $2 batas jumlah claim (aplikasi membatasi 1..3).
-- Satu statement sudah atomik dalam implicit transaction. COMMIT sebelum I/O Submit.
-- name: ClaimPending
WITH candidates AS (
 SELECT d.id FROM documents d JOIN batches b ON b.id=d.batch_id
 WHERE b.batch_no=$1 AND d.status='PENDING' AND d.attempt_count<d.max_attempts
 ORDER BY d.id LIMIT $2 FOR UPDATE OF d SKIP LOCKED
)
UPDATE documents d SET status='PROCESSING',attempt_count=d.attempt_count+1,
 locked_at=now(),updated_at=now(),last_error='',next_retry_at=NULL
WHERE d.id IN(SELECT id FROM candidates)
RETURNING d.*;

-- 6. schema.sql memasang CONSTRAINT document_callbacks_callback_id_key UNIQUE(callback_id).
-- Untuk skema awal PDF, DDL ekuivalennya (jalankan sekali, bukan setiap callback):
-- ALTER TABLE document_callbacks ADD CONSTRAINT document_callbacks_callback_id_key UNIQUE(callback_id);
-- Callback ID diasumsikan unik global per Principal, bukan hanya per dokumen.
-- $1 document_id, $2 callback_id, $3 attempt_count, $4 status, $5 message.
-- RETURNING menghasilkan satu row = baru, zero row = duplikat.
-- Zero row harus dibandingkan dengan record existing: ID sama isi berbeda -> 409.
-- name: InsertCallback
INSERT INTO document_callbacks(document_id,callback_id,attempt_count,status,message)
VALUES($1,$2,$3,$4,$5) ON CONFLICT(callback_id) DO NOTHING RETURNING id;

-- 7. Alur transaksi lengkap. Aplikasi rollback jika dokumen tidak ada, future attempt,
-- invalid status, atau collision CallbackID. No I/O Principal di dalam transaksi.
BEGIN;
-- name: LockCallbackDocument
SELECT id,status,attempt_count FROM documents WHERE id=$1 FOR UPDATE;
-- Aplikasi memvalidasi attempt_count $3 <= attempt_count tersimpan sebelum insert.
-- SUCCESS ketika PENDING -> ROLLBACK + 409 tanpa record agar dapat redelivery.
-- $6: jadwal dari RetryPolicy untuk FAILED eligible, NULL untuk SUCCESS/exhausted.
-- name: ApplyCallback
WITH inserted AS (
 INSERT INTO document_callbacks(document_id,callback_id,attempt_count,status,message)
 VALUES($1,$2,$3,$4,$5) ON CONFLICT(callback_id) DO NOTHING RETURNING id
)
UPDATE documents SET status=$4,
 last_error=CASE WHEN $4='FAILED' THEN $5 ELSE '' END,
 locked_at=NULL,updated_at=now(),next_retry_at=$6
WHERE id=$1 AND status<>'SUCCESS'
 AND (($4='SUCCESS' AND $3<=attempt_count) OR ($4='FAILED' AND $3=attempt_count))
 AND (status='PROCESSING' OR (status='FAILED' AND $4='SUCCESS'))
 AND EXISTS(SELECT 1 FROM inserted)
RETURNING id,status,attempt_count;
-- Jika insert duplikat, pastikan semua field sama. Jika berbeda: ROLLBACK + conflict.
-- name: ReadCallback
SELECT callback_id,document_id,attempt_count,status,message
FROM document_callbacks WHERE callback_id=$1;
COMMIT;

-- 8. Index di schema.sql. Leading batch_id untuk equality batch, lalu status,
-- lalu id untuk urutan claim/list yang stabil. batch_no UNIQUE sudah punya btree.
-- name: BatchStatusIndex
CREATE INDEX IF NOT EXISTS documents_batch_status_id_idx ON documents(batch_id,status,id);
-- Alternatif untuk tabel dengan SUCCESS dominan: lebih kecil, tapi tidak membantu
-- pencarian PROCESSING. Jangan memasang keduanya tanpa pengukuran EXPLAIN BUFFERS.
-- CREATE INDEX documents_retry_queue_idx ON documents(batch_id,status,id)
-- WHERE status IN ('PENDING','FAILED');
-- Tiap pergantian status mengubah index dan WAL, mengurangi peluang HOT update.
-- Lease scan memakai partial index documents_processing_lease_idx(locked_at,id).
-- schema.sql juga memasang documents_due_retry_idx(batch_id,next_retry_at,id)
-- untuk FAILED dengan attempt_count<max_attempts.

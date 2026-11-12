-- Initial schema. Apply once to a new database; no destructive reset on startup.
CREATE TABLE batches (
 id BIGSERIAL PRIMARY KEY,
 batch_no VARCHAR(64) NOT NULL UNIQUE CHECK (length(trim(batch_no)) > 0),
 total_documents INT NOT NULL CHECK (total_documents BETWEEN 1 AND 1000),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Batch status is derived from documents to avoid a stale aggregate.
CREATE TABLE documents (
 id BIGSERIAL PRIMARY KEY,
 batch_id BIGINT NOT NULL REFERENCES batches(id),
 document_no VARCHAR(64) NOT NULL CHECK (length(trim(document_no)) > 0),
 payload JSONB NOT NULL CHECK (payload <> 'null'::jsonb),
 status VARCHAR(16) NOT NULL DEFAULT 'PENDING'
   CHECK (status IN ('PENDING','PROCESSING','SUCCESS','FAILED')),
 attempt_count INT NOT NULL DEFAULT 0,
 max_attempts INT NOT NULL DEFAULT 3 CHECK (max_attempts = 3),
 request_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
 last_error TEXT NOT NULL DEFAULT '',
 locked_at TIMESTAMPTZ,
 next_retry_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(batch_id,document_no),
 CHECK (attempt_count BETWEEN 0 AND max_attempts),
 CHECK (status = 'PENDING' OR attempt_count > 0),
 CHECK (status <> 'PENDING' OR attempt_count < max_attempts),
 CHECK ((status = 'PROCESSING') = (locked_at IS NOT NULL)),
 CONSTRAINT documents_retry_schedule_check
 CHECK ((status = 'FAILED' AND attempt_count < max_attempts) = (next_retry_at IS NOT NULL))
);
CREATE TABLE document_callbacks (
 id BIGSERIAL PRIMARY KEY,
 document_id BIGINT NOT NULL REFERENCES documents(id),
 callback_id VARCHAR(128) NOT NULL CHECK (length(trim(callback_id)) > 0),
 attempt_count INT NOT NULL CHECK (attempt_count BETWEEN 1 AND 3),
 status VARCHAR(16) NOT NULL CHECK (status IN ('SUCCESS','FAILED')),
 message TEXT NOT NULL DEFAULT '',
 received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CONSTRAINT document_callbacks_callback_id_key UNIQUE(callback_id)
);
CREATE INDEX documents_batch_status_id_idx ON documents(batch_id,status,id);
CREATE INDEX documents_processing_lease_idx ON documents(locked_at,id) WHERE status='PROCESSING';
CREATE INDEX documents_due_retry_idx ON documents(batch_id,next_retry_at,id)
WHERE status='FAILED' AND attempt_count<max_attempts;
CREATE INDEX document_callbacks_document_idx ON document_callbacks(document_id,id);

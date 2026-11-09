CREATE TABLE IF NOT EXISTS queue_gate (
  id TINYINT UNSIGNED NOT NULL PRIMARY KEY
) ENGINE=InnoDB;
INSERT IGNORE INTO queue_gate (id) VALUES (1);
CREATE TABLE IF NOT EXISTS jobs (
  id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  prompt TEXT NOT NULL,
  idempotency_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL UNIQUE,
  status VARCHAR(16) NOT NULL,
  result MEDIUMTEXT NULL,
  error VARCHAR(1024) NULL,
  attempts INT NOT NULL DEFAULT 0,
  next_run_at DATETIME(6) NOT NULL,
  lease_until DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
  INDEX idx_due (status, next_run_at, id),
  INDEX idx_lease (status, lease_until),
  INDEX idx_created (created_at, id)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

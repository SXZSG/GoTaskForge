module gotaskforge

go 1.23

// Only the full MySQL build (-tags mysql) requires this driver.
// The offline file-store build needs no external Go dependencies.
require github.com/go-sql-driver/mysql v1.9.3

require filippo.io/edwards25519 v1.1.0 // indirect

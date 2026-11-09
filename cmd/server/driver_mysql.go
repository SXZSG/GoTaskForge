//go:build mysql

package main

// Standard database/sql uses this driver only in the full MySQL build.
import _ "github.com/go-sql-driver/mysql"

package services

import (
	"database/sql"

	"github.com/daodao97/xgo/xdb"
)

// ensureBlacklistTables 初始化黑名单相关的数据库表
func ensureBlacklistTables() error {
	db, err := xdb.DB("default")
	if err != nil {
		return err
	}
	return ensureBlacklistTablesWithDB(db)
}

// ensureBlacklistTablesWithDB 使用给定的数据库连接初始化黑名单表
func ensureBlacklistTablesWithDB(db *sql.DB) error {
	// 创建 provider_blacklist 表
	const createBlacklistTableSQL = `CREATE TABLE IF NOT EXISTS provider_blacklist (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		platform TEXT NOT NULL,
		provider_name TEXT NOT NULL,
		failure_count INTEGER DEFAULT 1,
		blacklisted_at DATETIME,
		blacklisted_until DATETIME,
		last_failure_at DATETIME,
		auto_recovered BOOLEAN DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,

		-- 等级拉黑新增字段（v0.4.0）
		blacklist_level INTEGER DEFAULT 0,
		last_recovered_at DATETIME,
		last_degrade_hour INTEGER DEFAULT 0,
		last_failure_window_start DATETIME,

		UNIQUE(platform, provider_name)
	)`

	if _, err := db.Exec(createBlacklistTableSQL); err != nil {
		return err
	}

	// 兼容升级：为旧表添加新字段（如果表已存在）
	alterTableStatements := []string{
		"ALTER TABLE provider_blacklist ADD COLUMN blacklist_level INTEGER DEFAULT 0",
		"ALTER TABLE provider_blacklist ADD COLUMN last_recovered_at DATETIME",
		"ALTER TABLE provider_blacklist ADD COLUMN last_degrade_hour INTEGER DEFAULT 0",
		"ALTER TABLE provider_blacklist ADD COLUMN last_failure_window_start DATETIME",
	}

	for _, stmt := range alterTableStatements {
		// 忽略错误（字段可能已存在）
		db.Exec(stmt)
	}

	// 创建 app_settings 表
	const createSettingsTableSQL = `CREATE TABLE IF NOT EXISTS app_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`

	if _, err := db.Exec(createSettingsTableSQL); err != nil {
		return err
	}

	// 插入默认配置（如果不存在）
	const insertDefaultSettings = `
		INSERT OR IGNORE INTO app_settings (key, value) VALUES
			('blacklist_failure_threshold', '3'),
			('blacklist_duration_minutes', '30'),
			('enable_blacklist', 'true')
	`

	if _, err := db.Exec(insertDefaultSettings); err != nil {
		return err
	}

	return nil
}

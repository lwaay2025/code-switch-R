package services

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/daodao97/xgo/xdb"
)

// ============================================================
// 表结构定义
// ============================================================

// ensureCaptureTables 创建所有抓包相关的表（幂等）
func ensureCaptureTables() error {
	db, err := xdb.DB("default")
	if err != nil {
		return fmt.Errorf("获取数据库连接失败: %w", err)
	}

	tables := []struct {
		name string
		sql  string
	}{
		{
			"capture_activity",
			"CREATE TABLE IF NOT EXISTS capture_activity (id INTEGER PRIMARY KEY AUTOINCREMENT,activity_id TEXT NOT NULL UNIQUE,source TEXT NOT NULL DEFAULT 'codex',first_seen_at DATETIME DEFAULT CURRENT_TIMESTAMP,last_seen_at DATETIME DEFAULT CURRENT_TIMESTAMP,turn_count INTEGER DEFAULT 0,request_count INTEGER DEFAULT 0,status TEXT DEFAULT 'active')",
		},
		{
			"capture_turn",
			"CREATE TABLE IF NOT EXISTS capture_turn (id INTEGER PRIMARY KEY AUTOINCREMENT,turn_id TEXT NOT NULL UNIQUE,activity_id TEXT NOT NULL,previous_turn_id TEXT DEFAULT '',source TEXT DEFAULT 'codex',model TEXT DEFAULT '',requested_at DATETIME,completed_at DATETIME,first_byte_at DATETIME,status TEXT DEFAULT 'pending',http_code INTEGER DEFAULT 0,total_duration_ms INTEGER DEFAULT 0,chunk_count INTEGER DEFAULT 0,total_bytes INTEGER DEFAULT 0,aggregated_response_text TEXT DEFAULT '')",
		},
		{
			"capture_request",
			"CREATE TABLE IF NOT EXISTS capture_request (id INTEGER PRIMARY KEY AUTOINCREMENT,request_id TEXT NOT NULL UNIQUE,turn_id TEXT NOT NULL,activity_id TEXT NOT NULL,timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,source TEXT DEFAULT 'codex',protocol TEXT DEFAULT 'HTTP',url TEXT DEFAULT '',method TEXT DEFAULT 'POST',request_headers TEXT DEFAULT '{}',request_body TEXT DEFAULT '',response_headers TEXT DEFAULT '{}',response_status_code INTEGER DEFAULT 0,response_summary TEXT DEFAULT '{}')",
		},
		{
			"capture_response_chunk",
			"CREATE TABLE IF NOT EXISTS capture_response_chunk (id INTEGER PRIMARY KEY AUTOINCREMENT,request_id TEXT NOT NULL,turn_id TEXT NOT NULL,chunk_index INTEGER NOT NULL,chunk_timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,chunk_content TEXT NOT NULL,chunk_type TEXT DEFAULT 'event')",
		},
		{
			"capture_ws_connection",
			"CREATE TABLE IF NOT EXISTS capture_ws_connection (id INTEGER PRIMARY KEY AUTOINCREMENT,connection_id TEXT NOT NULL UNIQUE,activity_id TEXT DEFAULT '',url TEXT DEFAULT '',opened_at DATETIME DEFAULT CURRENT_TIMESTAMP,closed_at DATETIME,status TEXT DEFAULT 'open')",
		},
		{
			"capture_ws_message",
			"CREATE TABLE IF NOT EXISTS capture_ws_message (id INTEGER PRIMARY KEY AUTOINCREMENT,connection_id TEXT NOT NULL,message_index INTEGER NOT NULL,direction TEXT NOT NULL,timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,content TEXT DEFAULT '',message_type TEXT DEFAULT 'text')",
		},
	}

	for _, t := range tables {
		if _, err := db.Exec(t.sql); err != nil {
			return fmt.Errorf("创建表 %s 失败: %w", t.name, err)
		}
		if err := ensureCaptureIndexes(db, t.name); err != nil {
			return fmt.Errorf("创建索引 %s 失败: %w", t.name, err)
		}
	}

	return nil
}

func ensureCaptureIndexes(db *sql.DB, table string) error {
	indexes := map[string][]string{
		"capture_activity": {
			"CREATE INDEX IF NOT EXISTS idx_ca_activity_id ON capture_activity(activity_id)",
			"CREATE INDEX IF NOT EXISTS idx_ca_last_seen ON capture_activity(last_seen_at)",
		},
		"capture_turn": {
			"CREATE INDEX IF NOT EXISTS idx_ct_turn_id ON capture_turn(turn_id)",
			"CREATE INDEX IF NOT EXISTS idx_ct_activity_id ON capture_turn(activity_id)",
			"CREATE INDEX IF NOT EXISTS idx_ct_requested_at ON capture_turn(requested_at)",
		},
		"capture_request": {
			"CREATE INDEX IF NOT EXISTS idx_cr_request_id ON capture_request(request_id)",
			"CREATE INDEX IF NOT EXISTS idx_cr_turn_id ON capture_request(turn_id)",
			"CREATE INDEX IF NOT EXISTS idx_cr_activity_id ON capture_request(activity_id)",
			"CREATE INDEX IF NOT EXISTS idx_cr_timestamp ON capture_request(timestamp)",
		},
		"capture_response_chunk": {
			"CREATE INDEX IF NOT EXISTS idx_crc_request_id ON capture_response_chunk(request_id)",
			"CREATE INDEX IF NOT EXISTS idx_crc_turn_id ON capture_response_chunk(turn_id)",
		},
		"capture_ws_connection": {
			"CREATE INDEX IF NOT EXISTS idx_cwc_connection_id ON capture_ws_connection(connection_id)",
		},
		"capture_ws_message": {
			"CREATE INDEX IF NOT EXISTS idx_cwm_connection_id ON capture_ws_message(connection_id)",
		},
	}

	if idxs, ok := indexes[table]; ok {
		for _, idx := range idxs {
			if _, err := db.Exec(idx); err != nil {
				return err
			}
		}
	}
	return nil
}

// ============================================================
// CRUD - Activity
// ============================================================

func InsertCaptureActivity(activityID, source string) error {
	err := execCaptureWrite("INSERT OR IGNORE INTO capture_activity (activity_id, source, first_seen_at, last_seen_at) VALUES (?, ?, datetime('now'), datetime('now'))", activityID, source)
	return err
}

func UpdateCaptureActivityLastSeen(activityID string) error {
	err := execCaptureWrite("UPDATE capture_activity SET last_seen_at = datetime('now') WHERE activity_id = ?", activityID)
	return err
}

func IncrementCaptureActivityCounters(activityID string, turnInc, reqInc int) error {
	err := execCaptureWrite("UPDATE capture_activity SET turn_count = turn_count + ?, request_count = request_count + ?, last_seen_at = datetime('now') WHERE activity_id = ?", turnInc, reqInc, activityID)
	return err
}

func SetCaptureActivityStatus(activityID, status string) error {
	err := execCaptureWrite("UPDATE capture_activity SET status = ? WHERE activity_id = ?", status, activityID)
	return err
}

// ============================================================
// CRUD - Turn
// ============================================================

func InsertCaptureTurn(turnID, activityID, prevTurnID, source, model string, requestedAt string) error {
	err := execCaptureWrite("INSERT OR IGNORE INTO capture_turn (turn_id, activity_id, previous_turn_id, source, model, requested_at, status) VALUES (?, ?, ?, ?, ?, ?, 'pending')", turnID, activityID, prevTurnID, source, model, requestedAt)
	return err
}

func UpdateCaptureTurnFirstByte(turnID string, firstByteAt string) error {
	err := execCaptureWrite("UPDATE capture_turn SET first_byte_at = ?, status = 'streaming' WHERE turn_id = ? AND first_byte_at IS NULL", firstByteAt, turnID)
	return err
}

func UpdateCaptureTurnAggregatedText(turnID, chunkContent string) error {
	err := execCaptureWrite("UPDATE capture_turn SET aggregated_response_text = aggregated_response_text || ? WHERE turn_id = ?", chunkContent, turnID)
	return err
}

func UpdateCaptureTurnSummary(turnID, status string, httpCode int, completedAt string, durationMs int, chunkCount int, totalBytes int) error {
	err := execCaptureWrite("UPDATE capture_turn SET status = ?, http_code = ?, completed_at = ?, total_duration_ms = ?, chunk_count = ?, total_bytes = ? WHERE turn_id = ?", status, httpCode, completedAt, durationMs, chunkCount, totalBytes, turnID)
	return err
}

// ============================================================
// CRUD - Request
// ============================================================

func InsertCaptureRequest(reqID, turnID, activityID, source, protocol, url, method, reqHeaders, reqBody string, timestamp string) error {
	err := execCaptureWrite("INSERT INTO capture_request (request_id, turn_id, activity_id, timestamp, source, protocol, url, method, request_headers, request_body) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", reqID, turnID, activityID, timestamp, source, protocol, url, method, reqHeaders, reqBody)
	return err
}

func UpdateCaptureRequestResponse(reqID, respHeaders string, statusCode int, summaryJSON string) error {
	err := execCaptureWrite("UPDATE capture_request SET response_headers = ?, response_status_code = ?, response_summary = ? WHERE request_id = ?", respHeaders, statusCode, summaryJSON, reqID)
	return err
}

// ============================================================
// CRUD - Response Chunk
// ============================================================

type ChunkRecord struct {
	RequestID string
	TurnID    string
	Index     int
	Content   string
	ChunkType string
}

func BulkInsertResponseChunks(chunks []ChunkRecord) error {
	if len(chunks) == 0 {
		return nil
	}
	valuePlaceholders := make([]string, 0, len(chunks))
	args := make([]interface{}, 0, len(chunks)*4)
	for _, c := range chunks {
		valuePlaceholders = append(valuePlaceholders, "(?, ?, ?, datetime('now'), ?, ?)")
		args = append(args, c.RequestID, c.TurnID, c.Index, c.Content, c.ChunkType)
	}
	sql := "INSERT INTO capture_response_chunk (request_id, turn_id, chunk_index, chunk_timestamp, chunk_content, chunk_type) VALUES " + strings.Join(valuePlaceholders, ", ")
	err := execCaptureWrite(sql, args...)
	return err
}

// ============================================================
// CRUD - WebSocket
// ============================================================

func InsertWSConnection(connID, activityID, url string) error {
	err := execCaptureWrite("INSERT OR IGNORE INTO capture_ws_connection (connection_id, activity_id, url, opened_at, status) VALUES (?, ?, ?, datetime('now'), 'open')", connID, activityID, url)
	return err
}

func UpdateWSConnectionClosed(connID string) error {
	err := execCaptureWrite("UPDATE capture_ws_connection SET closed_at = datetime('now'), status = 'closed' WHERE connection_id = ?", connID)
	return err
}

func InsertWSMessage(connID string, msgIndex int, direction, content, msgType string) error {
	err := execCaptureWrite("INSERT INTO capture_ws_message (connection_id, message_index, direction, timestamp, content, message_type) VALUES (?, ?, ?, datetime('now'), ?, ?)", connID, msgIndex, direction, content, msgType)
	return err
}

func execCaptureWrite(sql string, args ...interface{}) error {
	if GlobalDBQueue != nil {
		return GlobalDBQueue.Exec(sql, args...)
	}
	if GlobalDBQueueLogs != nil {
		return GlobalDBQueueLogs.ExecBatch(sql, args...)
	}
	return fmt.Errorf("数据库写入队列未初始化")
}

// ============================================================
// CRUD - Query
// ============================================================

func QueryCaptureActivities(limit, offset int) ([]map[string]string, error) {
	db, err := xdb.DB("default")
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT activity_id, source, first_seen_at, last_seen_at, turn_count, request_count, status FROM capture_activity ORDER BY last_seen_at DESC LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRowsToMaps(rows)
}

func QueryCaptureTurns(activityID string, limit, offset int) ([]map[string]string, error) {
	db, err := xdb.DB("default")
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT turn_id, activity_id, previous_turn_id, source, model, requested_at, completed_at, first_byte_at, status, http_code, total_duration_ms, chunk_count, total_bytes FROM capture_turn WHERE activity_id = ? ORDER BY requested_at DESC LIMIT ? OFFSET ?", activityID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRowsToMaps(rows)
}

func QueryCaptureRequests(turnID string, limit, offset int) ([]map[string]string, error) {
	db, err := xdb.DB("default")
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT request_id, turn_id, activity_id, timestamp, source, protocol, url, method, request_headers, request_body, response_headers, response_status_code, response_summary FROM capture_request WHERE turn_id = ? ORDER BY timestamp ASC LIMIT ? OFFSET ?", turnID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRowsToMaps(rows)
}

func QueryResponseChunks(requestID string, limit, offset int) ([]map[string]string, error) {
	db, err := xdb.DB("default")
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT id, chunk_index, chunk_timestamp, chunk_content, chunk_type FROM capture_response_chunk WHERE request_id = ? ORDER BY chunk_index ASC LIMIT ? OFFSET ?", requestID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRowsToMaps(rows)
}

func QueryWSConnections(limit, offset int) ([]map[string]string, error) {
	db, err := xdb.DB("default")
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT connection_id, activity_id, url, opened_at, closed_at, status FROM capture_ws_connection ORDER BY opened_at DESC LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRowsToMaps(rows)
}

func QueryWSMessages(connID string, limit, offset int) ([]map[string]string, error) {
	db, err := xdb.DB("default")
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT id, message_index, direction, timestamp, content, message_type FROM capture_ws_message WHERE connection_id = ? ORDER BY message_index ASC LIMIT ? OFFSET ?", connID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRowsToMaps(rows)
}

// ============================================================
// Search
// ============================================================

type CaptureSearchFilter struct {
	Source  string
	Status  string
	Keyword string
	Limit   int
	Offset  int
}

func SearchCaptureTurns(filter CaptureSearchFilter) ([]map[string]string, int, error) {
	db, err := xdb.DB("default")
	if err != nil {
		return nil, 0, err
	}
	var whereClauses []string
	var args []interface{}
	if filter.Source != "" {
		whereClauses = append(whereClauses, "ct.source = ?")
		args = append(args, filter.Source)
	}
	if filter.Status != "" {
		whereClauses = append(whereClauses, "ct.status = ?")
		args = append(args, filter.Status)
	}
	if filter.Keyword != "" {
		whereClauses = append(whereClauses, "(ct.turn_id LIKE ? OR ct.model LIKE ? OR cr.request_body LIKE ? OR cr.url LIKE ?)")
		kw := "%" + filter.Keyword + "%"
		args = append(args, kw, kw, kw, kw)
	}
	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}
	var total int
	countSQL := "SELECT COUNT(DISTINCT ct.id) FROM capture_turn ct LEFT JOIN capture_request cr ON cr.turn_id = ct.turn_id " + whereSQL
	if err := db.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := filter.Offset
	querySQL := "SELECT DISTINCT ct.turn_id, ct.activity_id, ct.previous_turn_id, ct.source, ct.model, ct.requested_at, ct.completed_at, ct.first_byte_at, ct.status, ct.http_code, ct.total_duration_ms, ct.chunk_count, ct.total_bytes FROM capture_turn ct LEFT JOIN capture_request cr ON cr.turn_id = ct.turn_id " + whereSQL + " ORDER BY ct.requested_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err := db.Query(querySQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	results, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, 0, err
	}
	return results, total, nil
}

// ============================================================
// Cleanup
// ============================================================

func DeleteCaptureOlderThan(days int) (int64, error) {
	db, err := xdb.DB("default")
	if err != nil {
		return 0, err
	}
	cutoff := fmt.Sprintf("-%d days", days)
	type tableInfo struct {
		name    string
		timeCol string
	}
	tables := []tableInfo{
		{"capture_response_chunk", "timestamp"},
		{"capture_request", "timestamp"},
		{"capture_turn", "requested_at"},
		{"capture_activity", "last_seen_at"},
		{"capture_ws_message", "timestamp"},
		{"capture_ws_connection", "opened_at"},
	}
	var totalDeleted int64
	for _, t := range tables {
		result, err := db.Exec(fmt.Sprintf("DELETE FROM %s WHERE %s < datetime('now', ?)", t.name, t.timeCol), cutoff)
		if err != nil {
			fmt.Printf("⚠️  清理表 %s 失败: %v\n", t.name, err)
			continue
		}
		n, _ := result.RowsAffected()
		totalDeleted += n
	}
	return totalDeleted, nil
}

func DeleteExcessCaptureRecords(maxRecords int) (int64, error) {
	if maxRecords <= 0 {
		return 0, nil
	}
	db, err := xdb.DB("default")
	if err != nil {
		return 0, err
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM capture_turn").Scan(&count); err != nil {
		return 0, err
	}
	if count <= maxRecords {
		return 0, nil
	}
	excess := count - maxRecords
	rows, err := db.Query("SELECT turn_id FROM capture_turn ORDER BY requested_at ASC LIMIT ?", excess)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var turnIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		turnIDs = append(turnIDs, id)
	}
	if len(turnIDs) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(turnIDs))
	args := make([]interface{}, len(turnIDs))
	for i, id := range turnIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	inClause := strings.Join(placeholders, ",")
	var total int64
	for _, table := range []string{"capture_response_chunk", "capture_request", "capture_turn"} {
		r, err := db.Exec(fmt.Sprintf("DELETE FROM %s WHERE turn_id IN (%s)", table, inClause), args...)
		if err == nil {
			n, _ := r.RowsAffected()
			total += n
		}
	}
	return total, nil
}

// ============================================================
// 辅助函数
// ============================================================

func scanRowsToMaps(rows *sql.Rows) ([]map[string]string, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var results []map[string]string
	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}
		row := make(map[string]string)
		for i, col := range columns {
			if values[i] != nil {
				switch v := values[i].(type) {
				case []byte:
					row[col] = string(v)
				default:
					row[col] = fmt.Sprintf("%v", v)
				}
			}
		}
		results = append(results, row)
	}
	if results == nil {
		results = make([]map[string]string, 0)
	}
	return results, nil
}

func DeleteCaptureActivity(activityID string) error {
	db, err := xdb.DB("default")
	if err != nil {
		return err
	}
	// Delete associated chunks
	_, _ = db.Exec(`DELETE FROM capture_response_chunk WHERE request_id IN (
		SELECT request_id FROM capture_request WHERE turn_id IN (
			SELECT turn_id FROM capture_turn WHERE activity_id = ?
		)
	)`, activityID)
	// Delete requests
	_, _ = db.Exec(`DELETE FROM capture_request WHERE turn_id IN (
		SELECT turn_id FROM capture_turn WHERE activity_id = ?
	)`, activityID)
	// Delete turns
	_, _ = db.Exec("DELETE FROM capture_turn WHERE activity_id = ?", activityID)
	// Delete activity itself
	_, err = db.Exec("DELETE FROM capture_activity WHERE activity_id = ?", activityID)
	return err
}

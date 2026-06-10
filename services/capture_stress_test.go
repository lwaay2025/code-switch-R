package services

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/daodao97/xgo/xdb"
	_ "modernc.org/sqlite"
)

// initTestDB sets up a test database and queue for stress tests
func initTestDB(t *testing.T) func() {
	tmpDir, err := os.MkdirTemp("", "capture-test-*")
	if err != nil {
		t.Fatalf("cannot create temp dir: %v", err)
	}
	dbPath := filepath.Join(tmpDir, "test.db?cache=shared&mode=rwc")

	// Register with xdb first (so capture_store.go's functions work)
	if err := xdb.Inits([]xdb.Config{
		{Name: "default", Driver: "sqlite", DSN: dbPath},
	}); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("xdb init failed: %v", err)
	}

	db, err := xdb.DB("default")
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("get db failed: %v", err)
	}
	db.SetMaxOpenConns(1)

	// Create all tables needed by capture_store CRUD functions
	mustExec(db, "PRAGMA journal_mode=WAL")
	mustExec(db, `CREATE TABLE IF NOT EXISTS capture_turn (turn_id TEXT PRIMARY KEY, activity_id TEXT, previous_turn_id TEXT, source TEXT, model TEXT, requested_at TEXT, status TEXT, http_code INTEGER, completed_at TEXT, total_duration_ms INTEGER DEFAULT 0, chunk_count INTEGER DEFAULT 0, total_bytes INTEGER DEFAULT 0, first_byte_at TEXT, aggregated_response_text TEXT)`)
	mustExec(db, `CREATE TABLE IF NOT EXISTS capture_request (request_id TEXT PRIMARY KEY, turn_id TEXT, activity_id TEXT, timestamp TEXT, source TEXT, protocol TEXT, url TEXT, method TEXT, request_headers TEXT, request_body TEXT, response_headers TEXT, response_status_code INTEGER DEFAULT 0, response_summary TEXT)`)
	mustExec(db, `CREATE TABLE IF NOT EXISTS capture_response_chunk (id INTEGER PRIMARY KEY AUTOINCREMENT, request_id TEXT, turn_id TEXT, chunk_index INTEGER, chunk_timestamp TEXT, chunk_content TEXT, chunk_type TEXT)`)
	mustExec(db, `CREATE TABLE IF NOT EXISTS capture_activity (activity_id TEXT PRIMARY KEY, source TEXT, first_seen_at TEXT, last_seen_at TEXT, turn_count INTEGER DEFAULT 0, request_count INTEGER DEFAULT 0, status TEXT)`)
	mustExec(db, `CREATE TABLE IF NOT EXISTS app_settings (key TEXT PRIMARY KEY, value TEXT)`)

	// Initialize the global write queues (uses xdb internally)
	InitGlobalDBQueue()

	return func() {
		shutdown := make(chan struct{})
		go func() {
			ShutdownGlobalDBQueue(10 * time.Second)
			close(shutdown)
		}()
		select {
		case <-shutdown:
		case <-time.After(15 * time.Second):
		}
		GlobalDBQueue = nil
		GlobalDBQueueLogs = nil
		os.RemoveAll(tmpDir)
	}
}

// TestFinalizeTurnHistoricalReplay reads historical captures from the scripts/app.db
// and replays them through FinalizeTurn to test for crashes under realistic workloads.
// NOTE: This test requires the scripts/app.db file to exist with capture data.
// It will SKIP if the database or data is not available.
func TestFinalizeTurnHistoricalReplay(t *testing.T) {
	InitCaptureManager()
	defer initTestDB(t)()
	globalCaptureEnabled.Store(true)
	GlobalCaptureManager.config.Enabled = true
	defer globalCaptureEnabled.Store(false)

	// Try to open the scripts/app.db from various possible working directories
	cwd, _ := os.Getwd()
	possiblePaths := []string{
		filepath.Join(cwd, "scripts", "app.db"),                    // running from project root
		filepath.Join(cwd, "..", "scripts", "app.db"),              // running from services/
		filepath.Join(cwd, "..", "..", "scripts", "app.db"),        // deeper nesting
	}
	_ = possiblePaths // will use below

	scriptDbPath := filepath.Join("..", "scripts", "app.db?cache=shared&mode=ro")
	sdb, err := sql.Open("sqlite", scriptDbPath)
	if err != nil {
		t.Skipf("cannot open scripts/app.db: %v", err)
	}
	defer sdb.Close()

	rows, err := sdb.Query(`
		SELECT ct.turn_id, ct.activity_id, ct.previous_turn_id, 
		       ct.source, ct.model,
		       COALESCE(cr.request_id, '') as request_id,
		       COALESCE(cr.url, '') as url,
		       COALESCE(cr.method, '') as method,
		       COALESCE(cr.request_headers, '') as request_headers,
		       COALESCE(cr.request_body, '') as request_body
		FROM capture_turn ct
		LEFT JOIN capture_request cr ON cr.turn_id = ct.turn_id
		ORDER BY ct.requested_at DESC
		LIMIT 50
	`)
	if err != nil {
		t.Skipf("query scripts/app.db failed: %v", err)
	}
	defer rows.Close()

	type htT struct {
		turnID, activityID, prevTurnID string
		source, model                  string
		requestID, url, method, reqHeaders, reqBody string
	}

	var turns []htT
	for rows.Next() {
		var ht htT
		var ts sql.NullString
		err := rows.Scan(&ht.turnID, &ht.activityID, &ht.prevTurnID, &ht.source, &ht.model, &ts, &ht.requestID, &ht.url, &ht.method, &ht.reqHeaders, &ht.reqBody)
		if err != nil { t.Logf("skip: %v", err); continue }
		turns = append(turns, ht)
	}
	if len(turns) == 0 { t.Skip("no historical data in scripts/app.db") }

	t.Logf("FOUND %d historical turns from scripts/app.db", len(turns))
	t.Logf("Starting replay: 3 rounds x 3x concurrent FinalizeTurn calls each")

	for round := 1; round <= 3; round++ {
		t.Run(fmt.Sprintf("round-%d", round), func(tt *testing.T) {
			var wg sync.WaitGroup
			for i, ht := range turns {
				wg.Add(1)
				go func(idx int, ht htT) {
					defer wg.Done()
					defer func() {
						if r := recover(); r != nil {
							tt.Errorf("panic round=%d turn=%s: %v", round, ht.turnID, r)
						}
					}()
					rid := ht.requestID
					if rid == "" {
						rid = fmt.Sprintf("rr-%d-%d", round, idx)
					}
					s := NewCaptureSession(rid, ht.turnID, ht.activityID, ht.prevTurnID)
					s.Source = ht.source
					s.URL = ht.url
					s.Method = ht.method
					s.ReqHeaders = ht.reqHeaders
					s.ReqBody = ht.reqBody
					s.Model = ht.model
					s.Chunks = []CaptureChunk{{0, time.Time{}, "data: test", "text"}}
					GlobalCaptureManager.mu.Lock()
					GlobalCaptureManager.sessions[ht.turnID] = s
					GlobalCaptureManager.mu.Unlock()
					var iwg sync.WaitGroup
					for j := 0; j < 3; j++ {
						iwg.Add(1)
						go func(c int) {
							defer iwg.Done()
							defer func() {
								if r := recover(); r != nil {
									tt.Errorf("panic2 round=%d turn=%s: %v", round, ht.turnID, r)
								}
							}()
							st, cd := "success", 200
							if idx%2 == 0 { st, cd = "error", 502 }
							GlobalCaptureManager.FinalizeTurn(ht.turnID, st, cd,
								map[string]string{"Content-Type": "text/event-stream"})
						}(j)
					}
					iwg.Wait()
					GlobalCaptureManager.RemoveSession(ht.turnID)
				}(i, ht)
			}
			wg.Wait()
		})
	}

	t.Log("Historical replay test completed successfully")
}

func mustExec(db *sql.DB, sqlStr string) {
	if _, err := db.Exec(sqlStr); err != nil {
		panic(fmt.Sprintf("exec failed: %s: %v", sqlStr, err))
	}
}

// TestFinalizeTurnHighConcurrency stress tests FinalizeTurn with many concurrent turns.
// This test does NOT need historical data - it creates sessions programmatically.
func TestFinalizeTurnHighConcurrency(t *testing.T) {
	InitCaptureManager()
	defer initTestDB(t)()
	globalCaptureEnabled.Store(true)
	GlobalCaptureManager.config.Enabled = true
	defer globalCaptureEnabled.Store(false)

	const n = 20
	var wg sync.WaitGroup
	errChan := make(chan error, n*3)
	var mu sync.Mutex

	for i := 0; i < n; i++ {
		tid := fmt.Sprintf("stress-turn-%d", i)
		aid := fmt.Sprintf("stress-activity-%d", i)
		rid := fmt.Sprintf("stress-req-%d", i)

		session := NewCaptureSession(rid, tid, aid, "")
		session.Chunks = []CaptureChunk{{0, time.Time{}, "data: test", "text"}}

		GlobalCaptureManager.mu.Lock()
		GlobalCaptureManager.sessions[tid] = session
		GlobalCaptureManager.mu.Unlock()

		for j := 0; j < 3; j++ {
			wg.Add(1)
			go func(tid string, callIdx int) {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						mu.Lock()
						errChan <- fmt.Errorf("panic turn=%s call=%d: %v", tid, callIdx, r)
						mu.Unlock()
					}
				}()
				st, cd := "success", 200
				if callIdx%2 == 0 {
					st, cd = "error", 502
				}
				GlobalCaptureManager.FinalizeTurn(tid, st, cd, nil)
			}(tid, j)
		}
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("test timed out - possible deadlock")
	}

	close(errChan)
	for e := range errChan {
		if e != nil {
			t.Error(e)
		}
	}
}

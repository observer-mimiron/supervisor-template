package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type recordingConnector struct{ conn *recordingConn }

func (c recordingConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c recordingConnector) Driver() driver.Driver                        { return recordingDriver{} }

type recordingDriver struct{}

func (recordingDriver) Open(string) (driver.Conn, error) { return &recordingConn{}, nil }

type recordingConn struct {
	mu      sync.Mutex
	queries []string
}

func (c *recordingConn) record(query string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queries = append(c.queries, query)
}
func (c *recordingConn) Prepare(query string) (driver.Stmt, error) {
	return recordingStmt{conn: c, query: query}, nil
}
func (c *recordingConn) Close() error               { return nil }
func (c *recordingConn) Begin() (driver.Tx, error)  { return recordingTx{}, nil }
func (c *recordingConn) Ping(context.Context) error { return nil }
func (c *recordingConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return recordingTx{}, nil
}
func (c *recordingConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.record(query)
	return &recordingRows{columns: []string{"id", "user_id", "product_id", "quantity", "total_amount", "created_at", "updated_at"}, values: [][]driver.Value{{uint64(7), uint64(1), uint64(1), int64(2), "39.80", time.Now(), time.Now()}}}, nil
}
func (c *recordingConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.record(query)
	return recordingResult{}, nil
}

type recordingStmt struct {
	conn  *recordingConn
	query string
}

func (s recordingStmt) Close() error  { return nil }
func (s recordingStmt) NumInput() int { return -1 }
func (s recordingStmt) Exec([]driver.Value) (driver.Result, error) {
	s.conn.record(s.query)
	return recordingResult{}, nil
}
func (s recordingStmt) Query([]driver.Value) (driver.Rows, error) {
	s.conn.record(s.query)
	return &recordingRows{columns: []string{"id"}, values: [][]driver.Value{{uint64(7)}}}, nil
}

type recordingTx struct{}

func (recordingTx) Commit() error   { return nil }
func (recordingTx) Rollback() error { return nil }

type recordingResult struct{}

func (recordingResult) LastInsertId() (int64, error) { return 7, nil }
func (recordingResult) RowsAffected() (int64, error) { return 1, nil }

type recordingRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *recordingRows) Columns() []string { return r.columns }
func (r *recordingRows) Close() error      { return nil }
func (r *recordingRows) Next(dest []driver.Value) error {
	r.index++
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	return nil
}

func TestMySQLQueryAndInsertEmitRedactedGORMSpans(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer provider.Shutdown(context.Background())
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)

	conn := &recordingConn{}
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sql.OpenDB(recordingConnector{conn: conn}), SkipInitializeWithVersion: true}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewForDB(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx, span := provider.Tracer("mysql-test").Start(context.Background(), "case")
	if _, err := adapter.Query(ctx, []byte(`{"user_id":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Insert(ctx, []byte(`{"user_id":1,"product_id":1,"quantity":2,"total_amount":"39.80"}`), "case-1"); err != nil {
		t.Fatal(err)
	}
	span.End()

	var querySpan, insertSpan bool
	for _, ended := range exporter.GetSpans() {
		if strings.Contains(strings.ToLower(ended.Name), "select orders") {
			querySpan = true
		}
		if strings.Contains(strings.ToLower(ended.Name), "insert orders") {
			insertSpan = true
		}
		for _, attr := range ended.Attributes {
			if strings.Contains(attr.Value.AsString(), "39.80") || strings.Contains(attr.Value.AsString(), `"user_id":1`) {
				t.Fatalf("SQL parameter leaked in span %q: %v", ended.Name, attr)
			}
		}
	}
	if !querySpan || !insertSpan {
		t.Fatalf("query span=%v insert span=%v spans=%v", querySpan, insertSpan, exporter.GetSpans())
	}
}

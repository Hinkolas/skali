package sqlrunner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// testConn opens a plain connection to the server behind TEST_DATABASE_URL.
// The runner creates nothing it does not drop again, so it needs no
// migrated database of its own.
func testConn(t *testing.T) (*pgconn.PgConn, *bytes.Buffer) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping: TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	config, err := pgconn.ParseConfig(dsn)
	require.NoError(t, err)
	notices := &bytes.Buffer{}
	config.OnNotice = NoticeWriter(notices)
	conn, err := pgconn.ConnectConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn, notices
}

type document struct {
	Results []struct {
		Columns []struct {
			Name    string `json:"name"`
			Type    string `json:"type"`
			TypeOID uint32 `json:"type_oid"`
		} `json:"columns"`
		Rows         [][]json.RawMessage `json:"rows"`
		Command      *string             `json:"command"`
		RowsAffected int64               `json:"rows_affected"`
	} `json:"results"`
	Error *errorDocument `json:"error"`
}

func run(t *testing.T, conn *pgconn.PgConn, script string) (document, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), conn, script, &stdout, &stderr)
	var doc document
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &doc), "stdout must be one JSON document: %s", stdout.String())
	return doc, stderr.String(), code
}

func TestExecuteTypedValues(t *testing.T) {
	conn, _ := testConn(t)
	doc, stderr, code := run(t, conn, `SELECT 1::int4 AS n, 9007199254740993::int8 AS big, 1.50::numeric AS price,
		'NaN'::numeric AS nan, 'Infinity'::float8 AS inf, true AS yes, NULL::text AS nothing,
		'{"a": [1, 2]}'::jsonb AS doc, 'he said "hi"'::text AS quote, ARRAY[1,2] AS list, 1 AS n`)
	require.Equal(t, ExitOK, code, stderr)
	require.Nil(t, doc.Error)
	require.Len(t, doc.Results, 1)
	result := doc.Results[0]
	names := make([]string, 0, len(result.Columns))
	for _, column := range result.Columns {
		names = append(names, column.Name)
	}
	require.Equal(t, []string{"n", "big", "price", "nan", "inf", "yes", "nothing", "doc", "quote", "list", "n"}, names)
	require.Equal(t, "int4", result.Columns[0].Type)
	require.Equal(t, "_int4", result.Columns[9].Type)
	require.Len(t, result.Rows, 1)
	row := result.Rows[0]
	// Raw values keep their JSON types; large integers stay exact.
	want := []string{`1`, `9007199254740993`, `1.50`, `"NaN"`, `"Infinity"`, `true`, `null`,
		`{"a": [1, 2]}`, `"he said \"hi\""`, `"{1,2}"`, `1`}
	got := make([]string, 0, len(row))
	for _, value := range row {
		got = append(got, string(value))
	}
	require.Equal(t, want, got)
	require.Equal(t, "SELECT 1", *result.Command)
	require.EqualValues(t, 1, result.RowsAffected)
}

func TestExecuteMultipleStatements(t *testing.T) {
	conn, notices := testConn(t)
	doc, stderr, code := run(t, conn, `
		CREATE TEMP TABLE runner_items (id int PRIMARY KEY, label text);
		INSERT INTO runner_items VALUES (1, 'a'), (2, 'b');
		SELECT id, label FROM runner_items ORDER BY id;
		DO $$ BEGIN RAISE NOTICE 'hello from the script'; END $$;`)
	require.Equal(t, ExitOK, code, stderr)
	require.Nil(t, doc.Error)
	require.Len(t, doc.Results, 4)
	require.Equal(t, "CREATE TABLE", *doc.Results[0].Command)
	require.Empty(t, doc.Results[0].Columns)
	require.Equal(t, "INSERT 0 2", *doc.Results[1].Command)
	require.EqualValues(t, 2, doc.Results[1].RowsAffected)
	require.Len(t, doc.Results[2].Rows, 2)
	require.Equal(t, `"b"`, string(doc.Results[2].Rows[1][1]))
	require.Contains(t, notices.String(), "NOTICE:  hello from the script")
}

func TestExecuteErrorStopsTheScript(t *testing.T) {
	conn, _ := testConn(t)
	doc, stderr, code := run(t, conn, "SELECT 1; SELECT * FROM runner_missing_table; SELECT 3")
	require.Equal(t, ExitScript, code)
	require.Len(t, doc.Results, 1, "statements after the failure must not run")
	require.NotNil(t, doc.Error)
	require.Equal(t, "42P01", doc.Error.Code)
	require.Equal(t, "ERROR", doc.Error.Severity)
	require.Positive(t, doc.Error.Position)
	require.Contains(t, stderr, `ERROR:  relation "runner_missing_table" does not exist`)

	// The connection stays usable after a script error.
	doc, _, code = run(t, conn, "SELECT 2")
	require.Equal(t, ExitOK, code)
	require.Len(t, doc.Results, 1)
}

func TestExecuteErrorMidResult(t *testing.T) {
	conn, _ := testConn(t)
	doc, _, code := run(t, conn, "SELECT 1 / (2 - g) FROM generate_series(1, 3) g")
	require.Equal(t, ExitScript, code)
	require.Len(t, doc.Results, 1)
	require.Nil(t, doc.Results[0].Command, "a failed statement carries no command tag")
	require.Equal(t, "22012", doc.Error.Code)
}

func TestExecuteEmptyScript(t *testing.T) {
	conn, _ := testConn(t)
	doc, _, code := run(t, conn, " \n\t")
	require.Equal(t, ExitOK, code)
	require.Empty(t, doc.Results)
	require.Nil(t, doc.Error)
}

func TestWriteValueFallbacks(t *testing.T) {
	render := func(oid uint32, value []byte) string {
		var buf bytes.Buffer
		out := bufio.NewWriter(&buf)
		writeValue(out, oid, value)
		require.NoError(t, out.Flush())
		return buf.String()
	}
	require.Equal(t, `null`, render(23, nil))
	require.Equal(t, `"-Infinity"`, render(701, []byte("-Infinity")))
	require.Equal(t, `1e+30`, render(701, []byte("1e+30")))
	require.Equal(t, `"maybe"`, render(16, []byte("maybe")))
	require.Equal(t, `"{not json"`, render(3802, []byte("{not json")))
	require.Equal(t, `"<b>"`, render(25, []byte("<b>")))
	require.True(t, strings.HasPrefix(render(25, []byte("x\ny")), `"x\ny"`))
}

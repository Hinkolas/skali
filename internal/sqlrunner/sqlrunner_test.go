package sqlrunner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
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
		CopyData     *string             `json:"copy_data"`
		CopyEncoding string              `json:"copy_encoding"`
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

func TestExecuteCopy(t *testing.T) {
	conn, _ := testConn(t)

	// COPY TO STDOUT keeps its output, and the script carries on after it.
	doc, stderr, code := run(t, conn, `COPY (SELECT g, 'row "'||g||'"' FROM generate_series(1, 2) g) TO STDOUT; SELECT 7`)
	require.Equal(t, ExitOK, code, stderr)
	require.Len(t, doc.Results, 2)
	require.NotNil(t, doc.Results[0].CopyData)
	require.Equal(t, "base64", doc.Results[0].CopyEncoding)
	text, err := base64.StdEncoding.DecodeString(*doc.Results[0].CopyData)
	require.NoError(t, err)
	require.Equal(t, "1\trow \"1\"\n2\trow \"2\"\n", string(text))
	require.Equal(t, "COPY 2", *doc.Results[0].Command)
	require.EqualValues(t, 2, doc.Results[0].RowsAffected)
	require.Equal(t, `7`, string(doc.Results[1].Rows[0][0]))

	// Binary COPY survives byte for byte as base64.
	doc, stderr, code = run(t, conn, "COPY (SELECT 7::int4) TO STDOUT (FORMAT BINARY)")
	require.Equal(t, ExitOK, code, stderr)
	require.Equal(t, "base64", doc.Results[0].CopyEncoding)
	raw, err := base64.StdEncoding.DecodeString(*doc.Results[0].CopyData)
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(raw, []byte("PGCOPY\n\xff\r\n\x00")), "the binary signature is intact: %q", raw)
	require.True(t, bytes.HasSuffix(raw, []byte{0xff, 0xff}), "the trailer is intact: %q", raw)
	require.True(t, bytes.Contains(raw, []byte{0, 0, 0, 4, 0, 0, 0, 7}), "the int4 field is intact: %q", raw)
	require.Empty(t, doc.Results[0].Rows)

	// Text COPY in its own ENCODING keeps its bytes too.
	doc, stderr, code = run(t, conn, "COPY (SELECT 'café'::text) TO STDOUT WITH (FORMAT CSV, ENCODING 'LATIN1')")
	require.Equal(t, ExitOK, code, stderr)
	latin, err := base64.StdEncoding.DecodeString(*doc.Results[0].CopyData)
	require.NoError(t, err)
	require.Equal(t, []byte("caf\xe9\n"), latin)

	// COPY FROM STDIN has no data to read: it fails at once, and nothing
	// after it runs.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	var stderrBuf bytes.Buffer
	code = Execute(ctx, conn, `CREATE TEMP TABLE copy_in (n int); COPY copy_in FROM STDIN; CREATE TEMP TABLE after_copy (n int)`,
		&stdout, &stderrBuf)
	require.Equal(t, ExitScript, code, stderrBuf.String())
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &doc))
	require.NotNil(t, doc.Error)
	require.Contains(t, doc.Error.Message, "COPY FROM STDIN is not supported by the json format")
	require.False(t, conn.IsClosed(), "the refusal leaves the connection usable")
	doc, _, code = run(t, conn, "SELECT to_regclass('after_copy') IS NULL AS skipped")
	require.Equal(t, ExitOK, code)
	require.Equal(t, `true`, string(doc.Results[0].Rows[0][0]), "statements after the refused COPY must not run")
}

func TestExecuteRefusesOtherClientEncodings(t *testing.T) {
	conn, _ := testConn(t)
	doc, stderr, code := run(t, conn, `SET client_encoding = LATIN1;
		SELECT convert_from(decode('636166c3a9', 'hex'), 'UTF8') AS label`)
	require.Equal(t, ExitScript, code, "mangled text must not pass as success")
	require.NotNil(t, doc.Error)
	require.Contains(t, doc.Error.Message, "needs client_encoding UTF8")
	require.Contains(t, stderr, "needs client_encoding UTF8")

	// Switching and switching back inside one script reports nothing (the
	// server reports parameters at the end of the query); the bytes give
	// it away.
	conn, _ = testConn(t)
	doc, _, code = run(t, conn, `SET client_encoding = LATIN1;
		SELECT convert_from(decode('636166c3a9', 'hex'), 'UTF8') AS label;
		SET client_encoding = UTF8`)
	require.Equal(t, ExitScript, code)
	require.Contains(t, doc.Error.Message, "needs client_encoding UTF8")
}

func TestExecuteFatalErrorIsAConnectionFailure(t *testing.T) {
	conn, _ := testConn(t)
	doc, stderr, code := run(t, conn, "SELECT pg_terminate_backend(pg_backend_pid())")
	require.Equal(t, ExitConnection, code, "a terminated backend is psql's status 2: %s", stderr)
	require.NotNil(t, doc.Error)
	require.Equal(t, "57P01", doc.Error.Code)
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
		_ = writeValue(out, oid, value)
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

// Package sqlrunner executes one SQL script against PostgreSQL and renders
// every statement's result as a single JSON document. It runs as `skalid
// sql-runner` inside the short-lived client pod of a `skali sql --format
// json` session, connected with the database's own login role from the
// standard PG* environment; the daemon never runs it itself.
//
// The script is sent as one simple-protocol query, exactly like `psql -c`:
// its statements run in one implicit transaction unless the script manages
// its own, and the first error stops it. psql meta-commands are not
// understood; the psql formats of skali sql exist for those.
package sqlrunner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// Exit statuses follow psql's, so a caller branches the same way whatever
// format it asked for: 2 when the database cannot be reached, 3 when the
// script failed, 1 for everything else that went wrong.
const (
	ExitOK         = 0
	ExitFailure    = 1
	ExitConnection = 2
	ExitScript     = 3
)

// maxScriptBytes bounds the script read from stdin. Results stream; only
// the script itself is held in memory.
const maxScriptBytes = 64 << 20

// Main is the sql-runner entry point: the script on stdin, the document on
// stdout, notices and diagnostics on stderr. It returns the exit status.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sql-runner", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	script, err := io.ReadAll(io.LimitReader(stdin, maxScriptBytes+1))
	if err != nil {
		fmt.Fprintf(stderr, "sql-runner: read the script: %v\n", err)
		return ExitFailure
	}
	if len(script) > maxScriptBytes {
		fmt.Fprintf(stderr, "sql-runner: the script exceeds %d MiB\n", maxScriptBytes>>20)
		return ExitFailure
	}

	config, err := pgconn.ParseConfig("")
	if err != nil {
		fmt.Fprintf(stderr, "sql-runner: %v\n", err)
		return ExitConnection
	}
	config.OnNotice = NoticeWriter(stderr)
	conn, err := connect(ctx, config)
	if err != nil {
		fmt.Fprintf(stderr, "sql-runner: connect: %v\n", err)
		return ExitConnection
	}
	defer conn.Close(context.WithoutCancel(ctx))

	return Execute(ctx, conn, string(script), stdout, stderr)
}

// connectWindow is how long a refused connection is retried: a fresh pod's
// first connections can be refused until the CNI's network policy
// enforcement registers its address (the same grace backup Jobs give
// pg_isready). The server rejecting the login is never retried.
const connectWindow = 30 * time.Second

func connect(ctx context.Context, config *pgconn.Config) (*pgconn.PgConn, error) {
	deadline := time.Now().Add(connectWindow)
	for {
		conn, err := pgconn.ConnectConfig(ctx, config)
		if err == nil {
			return conn, nil
		}
		if _, rejected := errors.AsType[*pgconn.PgError](err); rejected || time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(time.Second):
		}
	}
}

// NoticeWriter prints server notices (RAISE NOTICE, warnings) to w in
// psql's shape; they never enter the document.
func NoticeWriter(w io.Writer) pgconn.NoticeHandler {
	return func(_ *pgconn.PgConn, notice *pgconn.Notice) {
		fmt.Fprintf(w, "%s:  %s\n", notice.Severity, notice.Message)
	}
}

// Execute runs the script on conn and writes the document to stdout. The
// error, if any, is part of the document and repeated on stderr in psql's
// shape so a human reading a terminal sees it too.
func Execute(ctx context.Context, conn *pgconn.PgConn, script string, stdout, stderr io.Writer) int {
	out := bufio.NewWriter(stdout)
	defer out.Flush()

	_, _ = out.WriteString(`{"results":[`)
	var failure error
	if strings.TrimSpace(script) != "" {
		failure = writeResults(ctx, conn, script, out)
	}
	_, _ = out.WriteString("]")
	if failure != nil {
		_, _ = out.WriteString(`,"error":`)
		writeJSON(out, describeError(failure))
	}
	_, _ = out.WriteString("}\n")
	if err := out.Flush(); err != nil {
		fmt.Fprintf(stderr, "sql-runner: write the result: %v\n", err)
		return ExitFailure
	}

	if failure == nil {
		return ExitOK
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](failure); ok {
		fmt.Fprintf(stderr, "%s:  %s\n", pgErr.Severity, pgErr.Message)
		return ExitScript
	}
	fmt.Fprintf(stderr, "sql-runner: %v\n", failure)
	if conn.IsClosed() {
		return ExitConnection
	}
	return ExitScript
}

// column describes one result column.
type column struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	TypeOID uint32 `json:"type_oid"`
}

// errorDocument is the "error" member: the server's error fields when the
// database reported one, the message alone otherwise.
type errorDocument struct {
	Severity string `json:"severity,omitempty"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"`
	Hint     string `json:"hint,omitempty"`
	Position int32  `json:"position,omitempty"`
}

func describeError(err error) errorDocument {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return errorDocument{
			Severity: pgErr.Severity,
			Code:     pgErr.Code,
			Message:  pgErr.Message,
			Detail:   pgErr.Detail,
			Hint:     pgErr.Hint,
			Position: pgErr.Position,
		}
	}
	return errorDocument{Message: err.Error()}
}

// writeResults streams one result object per statement. Rows are arrays
// aligned with the columns (names repeat freely in SQL, so objects would
// lose values). The command tag comes last because the server sends it
// after the rows. A statement that fails ends its object without one.
func writeResults(ctx context.Context, conn *pgconn.PgConn, script string, out *bufio.Writer) error {
	types := pgtype.NewMap()
	results := conn.Exec(ctx, script)
	first := true
	var failure error
	for results.NextResult() {
		reader := results.ResultReader()
		if !first {
			_ = out.WriteByte(',')
		}
		first = false

		fields := reader.FieldDescriptions()
		columns := make([]column, len(fields))
		for i, field := range fields {
			columns[i] = column{Name: field.Name, TypeOID: field.DataTypeOID}
			if known, ok := types.TypeForOID(field.DataTypeOID); ok {
				columns[i].Type = known.Name
			}
		}
		_, _ = out.WriteString(`{"columns":`)
		writeJSON(out, columns)
		_, _ = out.WriteString(`,"rows":[`)
		for row := 0; reader.NextRow(); row++ {
			if row > 0 {
				_ = out.WriteByte(',')
			}
			_ = out.WriteByte('[')
			for i, value := range reader.Values() {
				if i > 0 {
					_ = out.WriteByte(',')
				}
				writeValue(out, fields[i].DataTypeOID, value)
			}
			_ = out.WriteByte(']')
		}
		_ = out.WriteByte(']')
		tag, err := reader.Close()
		if err != nil {
			_ = out.WriteByte('}')
			failure = err
			break
		}
		_, _ = out.WriteString(`,"command":`)
		writeJSON(out, tag.String())
		_, _ = out.WriteString(`,"rows_affected":`)
		_, _ = out.WriteString(strconv.FormatInt(tag.RowsAffected(), 10))
		_ = out.WriteByte('}')
	}
	if err := results.Close(); err != nil && failure == nil {
		failure = err
	}
	return failure
}

// writeValue renders one text-format value. Booleans, numbers, and JSON
// keep their JSON types; numbers JSON cannot express (NaN, Infinity) and
// every other type stay strings in PostgreSQL's own text form, which is
// lossless. NULL is null.
func writeValue(out *bufio.Writer, oid uint32, value []byte) {
	if value == nil {
		_, _ = out.WriteString("null")
		return
	}
	switch oid {
	case pgtype.BoolOID:
		switch string(value) {
		case "t":
			_, _ = out.WriteString("true")
			return
		case "f":
			_, _ = out.WriteString("false")
			return
		}
	case pgtype.Int2OID, pgtype.Int4OID, pgtype.Int8OID, pgtype.OIDOID,
		pgtype.Float4OID, pgtype.Float8OID, pgtype.NumericOID:
		if isJSONNumber(value) {
			_, _ = out.Write(value)
			return
		}
	case pgtype.JSONOID, pgtype.JSONBOID:
		if json.Valid(value) {
			_, _ = out.Write(value)
			return
		}
	}
	writeJSON(out, string(value))
}

func isJSONNumber(value []byte) bool {
	if len(value) == 0 || (value[0] != '-' && (value[0] < '0' || value[0] > '9')) {
		return false
	}
	return json.Valid(value)
}

// writeJSON encodes one value without HTML escaping: SQL text is full of
// < and > and the document never lands in a page.
func writeJSON(out *bufio.Writer, value any) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		// Every value here is a string, a struct of strings, or a slice of
		// them; encoding cannot fail on those.
		_, _ = out.WriteString("null")
		return
	}
	_, _ = out.Write(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
}

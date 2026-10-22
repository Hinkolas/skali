// Package sqlrunner executes one SQL script against PostgreSQL and renders
// every statement's result as a single JSON document. It runs as `skalid
// sql-runner` inside the short-lived client pod of a `skali sql --format
// json` session, connected with the database's own login role from the
// standard PG* environment; the daemon never runs it itself.
//
// The script is sent as one simple-protocol query, exactly like `psql -c`:
// its statements run in one implicit transaction unless the script manages
// its own, and the first error stops it. psql meta-commands are not
// understood, and COPY FROM STDIN is refused (stdin was the script); the
// psql formats of skali sql exist for those.
package sqlrunner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
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
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
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
	// JSON strings are UTF-8: ask for it whatever the role or database
	// defaults to.
	config.RuntimeParams["client_encoding"] = "UTF8"
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
	} else {
		fmt.Fprintf(stderr, "sql-runner: %v\n", failure)
	}
	// A FATAL error (a terminated backend, a retired login role) ends the
	// connection, not just the script: psql reports that as 2.
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

// errNotUTF8 fails a document whose text did not arrive as UTF-8 (a
// script that changed client_encoding): JSON cannot carry it faithfully.
var errNotUTF8 = errors.New("the json format needs client_encoding UTF8; text in the results is not reliable")

// copyFromStdinRefusal is the CopyFail message that answers COPY ... FROM
// STDIN: the runner's stdin was the script, so there is no data to send.
// The server turns it into an error that ends the script.
const copyFromStdinRefusal = "COPY FROM STDIN is not supported by the json format; " +
	"use the psql format (\\copy) or INSERT statements"

// writeResults sends the script as one simple query and streams one result
// object per statement, reading the protocol itself: pgconn's result reader
// skips COPY traffic, which would drop COPY TO STDOUT output silently and
// leave COPY FROM STDIN waiting for data. Rows are arrays aligned with the
// columns (names repeat freely in SQL, so objects would lose values).
// COPY TO STDOUT output is kept as base64 copy_data. The command tag
// comes last because the server sends it after the rows; a statement that
// fails ends its object without one.
func writeResults(ctx context.Context, conn *pgconn.PgConn, script string, out *bufio.Writer) error {
	frontend := conn.Frontend()
	frontend.Send(&pgproto3.Query{String: script})
	if err := frontend.Flush(); err != nil {
		return err
	}
	types := pgtype.NewMap()
	result := resultWriter{out: out}
	var failure error
	for {
		message, err := conn.ReceiveMessage(ctx)
		if err != nil {
			result.abandon()
			return err
		}
		switch message := message.(type) {
		case *pgproto3.RowDescription:
			columns := make([]column, len(message.Fields))
			oids := make([]uint32, len(message.Fields))
			for i, field := range message.Fields {
				columns[i] = column{Name: string(field.Name), TypeOID: field.DataTypeOID}
				if known, ok := types.TypeForOID(field.DataTypeOID); ok {
					columns[i].Type = known.Name
				}
				oids[i] = field.DataTypeOID
			}
			result.begin(columns, oids)
		case *pgproto3.DataRow:
			result.row(message.Values)
		case *pgproto3.CopyOutResponse:
			result.beginCopy()
		case *pgproto3.CopyData:
			result.copyData(message.Data)
		case *pgproto3.CopyInResponse:
			frontend.Send(&pgproto3.CopyFail{Message: copyFromStdinRefusal})
			if err := frontend.Flush(); err != nil {
				result.abandon()
				return err
			}
		case *pgproto3.CommandComplete:
			result.complete(pgconn.NewCommandTag(string(message.CommandTag)))
		case *pgproto3.ParameterStatus:
			// JSON strings are UTF-8; text in another client encoding
			// would come out mangled, so a script that switches is told
			// so instead of getting a document that looks fine. The server
			// reports changes only at the end of the query, so a switch
			// and back again shows up only in the bytes (invalidText).
			if message.Name == "client_encoding" && !strings.EqualFold(message.Value, "UTF8") && failure == nil {
				failure = fmt.Errorf("%w, and the script set %s", errNotUTF8, message.Value)
			}
		case *pgproto3.ErrorResponse:
			result.abandon()
			// The first server error is what stopped the script; it
			// outranks an earlier encoding complaint.
			if _, isServerError := failure.(*pgconn.PgError); !isServerError {
				failure = pgconn.ErrorResponseToPgError(message)
			}
		case *pgproto3.ReadyForQuery:
			if result.invalidText && failure == nil {
				failure = errNotUTF8
			}
			return failure
		}
	}
}

// resultWriter streams the results array one object at a time: open
// between a statement's first message and its command tag (or error).
type resultWriter struct {
	out     *bufio.Writer
	count   int
	open    bool
	oids    []uint32
	rows    int
	inCopy  bool
	hasCopy bool
	// invalidText records a text value that was not UTF-8.
	invalidText bool
	// copy encodes COPY output as base64: binary COPY is not text at all,
	// and text COPY may name its own ENCODING, so only bytes are lossless.
	copy io.WriteCloser
}

func (w *resultWriter) begin(columns []column, oids []uint32) {
	if w.count > 0 {
		_ = w.out.WriteByte(',')
	}
	w.count++
	w.open, w.oids, w.rows, w.inCopy, w.hasCopy, w.copy = true, oids, 0, false, false, nil
	_, _ = w.out.WriteString(`{"columns":`)
	writeJSON(w.out, columns)
	_, _ = w.out.WriteString(`,"rows":[`)
}

func (w *resultWriter) row(values [][]byte) {
	if !w.open {
		w.begin([]column{}, nil)
	}
	if w.rows > 0 {
		_ = w.out.WriteByte(',')
	}
	w.rows++
	_ = w.out.WriteByte('[')
	for i, value := range values {
		if i > 0 {
			_ = w.out.WriteByte(',')
		}
		var oid uint32
		if i < len(w.oids) {
			oid = w.oids[i]
		}
		if !writeValue(w.out, oid, value) {
			w.invalidText = true
		}
	}
	_ = w.out.WriteByte(']')
}

func (w *resultWriter) beginCopy() {
	if !w.open {
		w.begin([]column{}, nil)
	}
	_, _ = w.out.WriteString(`],"copy_encoding":"base64","copy_data":"`)
	w.copy = base64.NewEncoder(base64.StdEncoding, w.out)
	w.inCopy, w.hasCopy = true, true
}

// copyData appends one CopyData message to copy_data.
func (w *resultWriter) copyData(data []byte) {
	if w.inCopy {
		_, _ = w.copy.Write(data)
	}
}

// closeBody ends the rows array or the copy_data string.
func (w *resultWriter) closeBody() {
	if w.hasCopy {
		if w.inCopy {
			_ = w.copy.Close()
			w.copy = nil
			_ = w.out.WriteByte('"')
			w.inCopy = false
		}
		return
	}
	_ = w.out.WriteByte(']')
}

func (w *resultWriter) complete(tag pgconn.CommandTag) {
	if !w.open {
		w.begin([]column{}, nil)
	}
	w.closeBody()
	_, _ = w.out.WriteString(`,"command":`)
	writeJSON(w.out, tag.String())
	_, _ = w.out.WriteString(`,"rows_affected":`)
	_, _ = w.out.WriteString(strconv.FormatInt(tag.RowsAffected(), 10))
	_ = w.out.WriteByte('}')
	w.open = false
}

// abandon ends an open object without a command tag: its statement failed.
func (w *resultWriter) abandon() {
	if !w.open {
		return
	}
	w.closeBody()
	_ = w.out.WriteByte('}')
	w.open = false
}

// writeValue renders one text-format value. Booleans, numbers, and JSON
// keep their JSON types; numbers JSON cannot express (NaN, Infinity) and
// every other type stay strings in PostgreSQL's own text form, which is
// lossless. NULL is null. It reports false for text that is not UTF-8,
// which the JSON string cannot hold as it arrived.
func writeValue(out *bufio.Writer, oid uint32, value []byte) bool {
	if value == nil {
		_, _ = out.WriteString("null")
		return true
	}
	switch oid {
	case pgtype.BoolOID:
		switch string(value) {
		case "t":
			_, _ = out.WriteString("true")
			return true
		case "f":
			_, _ = out.WriteString("false")
			return true
		}
	case pgtype.Int2OID, pgtype.Int4OID, pgtype.Int8OID, pgtype.OIDOID,
		pgtype.Float4OID, pgtype.Float8OID, pgtype.NumericOID:
		if isJSONNumber(value) {
			_, _ = out.Write(value)
			return true
		}
	case pgtype.JSONOID, pgtype.JSONBOID:
		if json.Valid(value) {
			_, _ = out.Write(value)
			return true
		}
	}
	writeJSON(out, string(value))
	return utf8.Valid(value)
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

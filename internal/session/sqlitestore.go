package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// schema es la tabla unica del log de eventos. Las columnas cubren el round-trip
// completo de SessionEvent: las fases siguientes leen de aqui (Messages, Epoch,
// PendingToolCalls). Es idempotente (IF NOT EXISTS). El unico orden total durable
// por sesion es (session_id, seq), por eso es la PRIMARY KEY.
const schema = `
CREATE TABLE IF NOT EXISTS events (
  session_id  TEXT    NOT NULL,
  seq         INTEGER NOT NULL,
  kind        TEXT    NOT NULL,
  has_message INTEGER NOT NULL,
  msg_id      TEXT,
  role        TEXT,
  text        TEXT,
  call_id     TEXT,
  tool_name   TEXT,
  input       BLOB,
  usage       BLOB,
  error       TEXT,
  tool_calls  BLOB,
  tool_call_id TEXT,
  ev_text     TEXT,
  diff        TEXT,
	compaction  BLOB,
  PRIMARY KEY (session_id, seq)
);

CREATE TABLE IF NOT EXISTS session_context (
  session_id   TEXT    NOT NULL PRIMARY KEY,
  agent        TEXT    NOT NULL DEFAULT '',
  model        TEXT    NOT NULL DEFAULT '',
  baseline_seq INTEGER NOT NULL DEFAULT 0,
  revision     INTEGER NOT NULL DEFAULT 0,
  checkpoint   BLOB
);`

// SQLiteStore implementa Store sobre SQLite via el driver puro Go modernc.org/sqlite.
type SQLiteStore struct {
	db *sql.DB
	mu sync.Mutex
}

var _ Store = (*SQLiteStore)(nil)
var _ CompactionStore = (*SQLiteStore)(nil)

// NewSQLiteStore abre (o crea) la base en dsn y asegura el esquema. dsn puede ser
// ":memory:" o una ruta de archivo. Para archivo construye un DSN URI del driver
// modernc con pragmas POR CONEXION: journal_mode(WAL) permite lectores y un
// escritor de procesos distintos (la TUI y la app Wails comparten el .db) y
// busy_timeout(5000) hace que un escritor espere el write-lock en vez de fallar
// con SQLITE_BUSY. Van via DSN y no con db.Exec post-open porque database/sql
// recicla conexiones del pool: un Exec unico perderia el busy_timeout en las
// conexiones nuevas; via DSN cada conexion lo aplica al abrirse. ":memory:"
// queda como esta (WAL no aplica a memoria y el pool esta clavado en 1 conexion).
func NewSQLiteStore(dsn string) (*SQLiteStore, error) {
	displayDSN := sqliteDisplayDSN(dsn)
	var err error
	dsn, err = sqliteDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("normalize sqlite DSN %q: %w", displayDSN, err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database %q: %w", displayDSN, err)
	}
	// Obligatorio: con ":memory:" cada conexion del pool tendria su PROPIA base y
	// se perderian datos entre llamadas. Ademas serializa el unico escritor.
	db.SetMaxOpenConns(1)
	if err := initializeSQLiteSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize sqlite schema for %q: %w", displayDSN, err)
	}
	return &SQLiteStore{db: db}, nil
}

func initializeSQLiteSchema(db *sql.DB) error {
	deadline := time.Now().Add(5 * time.Second)
	backoff := 5 * time.Millisecond
	for {
		err := migrateSQLiteSchema(db)
		if err == nil || !isSQLiteLockError(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(backoff)
		if backoff < 100*time.Millisecond {
			backoff *= 2
		}
	}
}

func migrateSQLiteSchema(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	columns, err := sqliteTableColumns(db, "events")
	if err != nil {
		return fmt.Errorf("inspect events schema: %w", err)
	}
	for _, column := range []struct {
		name       string
		definition string
	}{
		{"tool_calls", "BLOB"},
		{"tool_call_id", "TEXT"},
		{"ev_text", "TEXT"},
		{"diff", "TEXT"},
		{"compaction", "BLOB"},
	} {
		if columns[column.name] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE events ADD COLUMN ` + column.name + ` ` + column.definition); err != nil {
			columns, inspectErr := sqliteTableColumns(db, "events")
			if inspectErr != nil || !columns[column.name] {
				if inspectErr != nil {
					return fmt.Errorf("migrate events column %q: %v; re-inspect: %w", column.name, err, inspectErr)
				}
				return fmt.Errorf("migrate events column %q: %w", column.name, err)
			}
		}
	}
	return nil
}

func isSQLiteLockError(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	switch sqliteErr.Code() & 0xff {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return true
	default:
		return false
	}
}

func sqliteDisplayDSN(dsn string) string {
	if dsn == ":memory:" {
		return dsn
	}
	if strings.HasPrefix(dsn, "file:") {
		if parsed, err := url.Parse(dsn); err == nil {
			parsed.RawQuery = ""
			parsed.Fragment = ""
			return parsed.String()
		}
		if index := strings.IndexAny(dsn, "?#"); index >= 0 {
			return dsn[:index]
		}
	}
	return dsn
}

func sqliteDSN(dsn string) (string, error) {
	if dsn == ":memory:" {
		return dsn, nil
	}
	var parsed *url.URL
	var err error
	if strings.HasPrefix(dsn, "file:") {
		parsed, err = url.Parse(dsn)
	} else {
		parsed = &url.URL{Scheme: "file", Path: dsn}
	}
	if err != nil {
		return "", fmt.Errorf("parse sqlite dsn: %w", err)
	}
	query := parsed.Query()
	pragmas := query["_pragma"]
	query.Del("_pragma")
	for _, pragma := range pragmas {
		name := pragma
		if index := strings.IndexAny(name, "=("); index >= 0 {
			name = name[:index]
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "journal_mode", "busy_timeout":
		default:
			query.Add("_pragma", pragma)
		}
	}
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Set("_txlock", "immediate")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func sqliteTableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

// Close cierra la base subyacente.
func (s *SQLiteStore) Close() error { return s.db.Close() }

// DataVersion expone PRAGMA data_version: cambia cuando OTRA conexion
// (tipicamente otro proceso: la TUI) modifica la base, y NO cambia por las
// escrituras propias porque el pool esta clavado en 1 conexion ("propia
// conexion" == "propio store"). Por eso sirve como senal barata para detectar
// escrituras externas sin auto-dispararse por los appends propios.
func (s *SQLiteStore) DataVersion(ctx context.Context) (int64, error) {
	var v int64
	if err := s.db.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// AppendEvent agrega ev al log de sessionID, le asigna el siguiente Seq monotonico
// y lo devuelve. El calculo del Seq y el INSERT son UN SOLO statement (subquery
// MAX(seq)+1 con RETURNING): corre en su propia transaccion de escritura de
// SQLite, asi dos procesos sobre el mismo archivo quedan serializados por el
// write-lock (con busy_timeout esperan en vez de fallar) y no puede haber UNIQUE
// constraint por Seqs raceados. El mutex sigue serializando dentro del proceso.
func (s *SQLiteStore) AppendEvent(ctx context.Context, sessionID string, ev SessionEvent) (Seq, error) {
	if ev.Kind == KindContextCompacted || ev.Compaction != nil {
		return 0, ErrCompactionRequiresCommit
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	hasMessage := 0
	var msgID, role, text, toolCallID sql.NullString
	var toolCalls []byte
	if ev.Message != nil {
		hasMessage = 1
		msgID = sql.NullString{String: ev.Message.ID, Valid: true}
		role = sql.NullString{String: string(ev.Message.Role), Valid: true}
		text = sql.NullString{String: ev.Message.Text, Valid: true}
		toolCallID = sql.NullString{String: ev.Message.ToolCallID, Valid: true}
		if ev.Message.ToolCalls != nil {
			b, err := json.Marshal(ev.Message.ToolCalls)
			if err != nil {
				return 0, err
			}
			toolCalls = b
		}
	}

	var usage []byte
	if ev.Usage != nil {
		b, err := json.Marshal(ev.Usage)
		if err != nil {
			return 0, err
		}
		usage = b
	}
	var compaction []byte
	if ev.Compaction != nil {
		b, err := json.Marshal(ev.Compaction)
		if err != nil {
			return 0, err
		}
		compaction = b
	}

	// ev_text guarda el Text top-level del SessionEvent (Reasoning/Text.*,
	// Tool.Input.Delta), independiente de la columna text (que es Message.Text).
	// Sin esto la rehidratacion pierde el razonamiento y la respuesta del asistente,
	// que viajan en ev.Text y no en un Message.
	var seq int64
	if err := s.db.QueryRowContext(ctx,
		`INSERT INTO events
		   (session_id, seq, kind, has_message, msg_id, role, text, call_id, tool_name, input, usage, error, tool_calls, tool_call_id, ev_text, diff, compaction)
		 VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM events WHERE session_id = ?), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 RETURNING seq`,
		sessionID, sessionID, string(ev.Kind), hasMessage, msgID, role, text,
		ev.CallID, ev.ToolName, []byte(ev.Input), usage, ev.Error, toolCalls, toolCallID, ev.Text, ev.Diff, compaction,
	).Scan(&seq); err != nil {
		return 0, err
	}
	return Seq(seq), nil
}

// exists devuelve si la sesion recibio al menos un evento. Es el chequeo de
// existencia compartido por LoadSession/Messages/Epoch/PendingToolCalls para
// distinguir "sesion vacia" de "sesion inexistente".
func (s *SQLiteStore) exists(ctx context.Context, sessionID string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM events WHERE session_id = ? LIMIT 1`, sessionID,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// LoadSession devuelve el agregado de la sesion. ErrSessionNotFound si la sesion
// nunca recibio un evento.
func (s *SQLiteStore) LoadSession(ctx context.Context, sessionID string) (Session, error) {
	ok, err := s.exists(ctx, sessionID)
	if err != nil {
		return Session{}, err
	}
	if !ok {
		return Session{}, ErrSessionNotFound
	}
	return Session{ID: sessionID}, nil
}

// Messages reproyecta los mensajes de la sesion en orden de Seq y devuelve solo
// los materializados por eventos con Seq > sinceSeq. ErrSessionNotFound si la
// sesion no existe; una sesion existente sin mensajes devuelve un slice vacio.
func (s *SQLiteStore) Messages(ctx context.Context, sessionID string, sinceSeq Seq) ([]Message, error) {
	ok, err := s.exists(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrSessionNotFound
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT msg_id, role, text, seq, tool_calls, tool_call_id
		   FROM events
		  WHERE session_id = ? AND has_message = 1 AND seq > ?
		  ORDER BY seq`,
		sessionID, int64(sinceSeq),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Message, 0)
	for rows.Next() {
		var (
			id         string
			role       string
			text       string
			seq        int64
			toolCalls  []byte
			toolCallID sql.NullString
		)
		if err := rows.Scan(&id, &role, &text, &seq, &toolCalls, &toolCallID); err != nil {
			return nil, err
		}
		msg := Message{ID: id, Role: Role(role), Text: text, Seq: Seq(seq), ToolCallID: toolCallID.String}
		if len(toolCalls) > 0 {
			if err := json.Unmarshal(toolCalls, &msg.ToolCalls); err != nil {
				return nil, err
			}
		}
		out = append(out, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Sessions devuelve un resumen por sesion con al menos un evento, ordenado por
// actividad mas reciente primero. Ordena por MAX(rowid) DESC (el rowid implicito
// es monotonico con la insercion, esta tabla NO es WITHOUT ROWID), y para cada
// sesion toma como Title el texto del primer mensaje del usuario (menor seq,
// role=user, has_message=1), truncado. Title "" si la sesion no tiene aun uno.
func (s *SQLiteStore) Sessions(ctx context.Context) ([]SessionSummary, error) {
	// El titulo generado (ultimo Session.Title) gana sobre el primer mensaje del
	// usuario, que queda como fallback via COALESCE si aun no se genero ninguno.
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.session_id,
		        COALESCE(
		          (SELECT g.ev_text
		             FROM events g
		            WHERE g.session_id = e.session_id
		              AND g.kind = 'Session.Title'
		            ORDER BY g.seq DESC
		            LIMIT 1),
		          (SELECT u.text
		             FROM events u
		            WHERE u.session_id = e.session_id
		              AND u.has_message = 1
		              AND u.role = 'user'
		            ORDER BY u.seq
		            LIMIT 1)) AS title,
		        (SELECT c.ev_text
		           FROM events c
		          WHERE c.session_id = e.session_id
		            AND c.kind = 'Session.Cwd'
		          ORDER BY c.seq DESC
		          LIMIT 1) AS cwd
		   FROM events e
		  GROUP BY e.session_id
		  ORDER BY MAX(e.rowid) DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]SessionSummary, 0)
	for rows.Next() {
		var id string
		var title, cwd sql.NullString
		if err := rows.Scan(&id, &title, &cwd); err != nil {
			return nil, err
		}
		out = append(out, SessionSummary{ID: id, Title: truncateTitle(title.String), Cwd: cwd.String})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Events devuelve todos los SessionEvent durables de la sesion en orden de Seq
// con Seq > sinceSeq. ErrSessionNotFound si la sesion no existe. Reconstruye cada
// evento como el inverso de AppendEvent: rehidrata Kind, Message (con ToolCalls /
// ToolCallID), payload de streaming (Text, CallID, ToolName, Input, Error) y Usage.
func (s *SQLiteStore) Events(ctx context.Context, sessionID string, sinceSeq Seq) ([]SessionEvent, error) {
	ok, err := s.exists(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrSessionNotFound
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, kind, has_message, msg_id, role, text, call_id, tool_name,
		        input, usage, error, tool_calls, tool_call_id, ev_text, diff, compaction
		   FROM events
		  WHERE session_id = ? AND seq > ?
		  ORDER BY seq`,
		sessionID, int64(sinceSeq),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]SessionEvent, 0)
	for rows.Next() {
		var (
			seq                                       int64
			kind                                      string
			hasMessage                                int
			msgID, role, text, callID, toolName, tcID sql.NullString
			errText, evText, diff                     sql.NullString
			input, usage, toolCalls, compaction       []byte
		)
		if err := rows.Scan(&seq, &kind, &hasMessage, &msgID, &role, &text,
			&callID, &toolName, &input, &usage, &errText, &toolCalls, &tcID, &evText, &diff, &compaction); err != nil {
			return nil, err
		}

		ev := SessionEvent{
			SessionID: sessionID,
			Seq:       Seq(seq),
			Kind:      EventKind(kind),
			Text:      evText.String,
			CallID:    callID.String,
			ToolName:  toolName.String,
			Error:     errText.String,
			Diff:      diff.String,
		}
		if len(input) > 0 {
			ev.Input = json.RawMessage(input)
		}
		if hasMessage == 1 {
			// Message.Seq se deja en cero: AppendEvent no lo persiste (no hay
			// columna para el Seq del Message dentro del evento), y MemoryStore lo
			// guarda verbatim, asi que el round-trip de Events lo refleja igual. El
			// Seq del evento vive en ev.Seq; quien quiera Messages usa esa proyeccion.
			msg := Message{
				ID:         msgID.String,
				Role:       Role(role.String),
				Text:       text.String,
				ToolCallID: tcID.String,
			}
			if len(toolCalls) > 0 {
				if err := json.Unmarshal(toolCalls, &msg.ToolCalls); err != nil {
					return nil, err
				}
			}
			ev.Message = &msg
		}
		if len(usage) > 0 {
			var u Usage
			if err := json.Unmarshal(usage, &u); err != nil {
				return nil, err
			}
			ev.Usage = &u
		}
		if len(compaction) > 0 {
			var checkpoint CompactionCheckpoint
			if err := json.Unmarshal(compaction, &checkpoint); err != nil {
				return nil, fmt.Errorf("decode Context.Compacted payload for session %q seq %d: %w: %v", sessionID, seq, ErrInvalidCompactionCheckpoint, err)
			}
			if err := ValidateCompactionCheckpoint(checkpoint); err != nil {
				return nil, fmt.Errorf("validate Context.Compacted payload for session %q seq %d: %w", sessionID, seq, err)
			}
			ev.Compaction = &checkpoint
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Epoch devuelve la foto del contexto vigente de la sesion. Igual que MemoryStore,
// el epoch es cero estable: snapshot y recheck coinciden y el runner no reconstruye.
// ErrSessionNotFound si la sesion no existe. El driver real del epoch (mover
// Agent/Model/Revision/BaselineSeq por cambios de contexto) llega despues.
func (s *SQLiteStore) Epoch(ctx context.Context, sessionID string) (ContextEpoch, error) {
	ok, err := s.exists(ctx, sessionID)
	if err != nil {
		return ContextEpoch{}, err
	}
	if !ok {
		return ContextEpoch{}, ErrSessionNotFound
	}
	var epoch ContextEpoch
	err = s.db.QueryRowContext(ctx,
		`SELECT agent, model, baseline_seq, revision FROM session_context WHERE session_id = ?`, sessionID,
	).Scan(&epoch.Agent, &epoch.Model, &epoch.BaselineSeq, &epoch.Revision)
	if err == sql.ErrNoRows {
		return ContextEpoch{}, nil
	}
	return epoch, err
}

// ContextForRunner reconstruye el checkpoint durable y el sufijo literal que
// consume el runner. Bases anteriores a session_context conservan epoch cero y
// proyectan el historial completo.
func (s *SQLiteStore) ContextForRunner(ctx context.Context, sessionID string) (result RunnerContext, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("SQLiteStore.ContextForRunner session %q: %w", sessionID, err)
		}
	}()
	if err := ctx.Err(); err != nil {
		return RunnerContext{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return RunnerContext{}, err
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RunnerContext{}, err
	}
	defer tx.Rollback()

	if ok, err := sqliteSessionExists(ctx, tx, sessionID); err != nil {
		return RunnerContext{}, err
	} else if !ok {
		return RunnerContext{}, ErrSessionNotFound
	}

	result = RunnerContext{}
	var checkpointRaw []byte
	err = tx.QueryRowContext(ctx,
		`SELECT agent, model, baseline_seq, revision, checkpoint FROM session_context WHERE session_id = ?`, sessionID,
	).Scan(&result.Epoch.Agent, &result.Epoch.Model, &result.Epoch.BaselineSeq, &result.Epoch.Revision, &checkpointRaw)
	if err != nil && err != sql.ErrNoRows {
		return RunnerContext{}, err
	}
	if len(checkpointRaw) == 0 {
		messages, err := sqliteMessages(ctx, tx, sessionID, result.Epoch.BaselineSeq)
		if err != nil {
			return RunnerContext{}, err
		}
		result.Messages = messages
		return result, tx.Commit()
	}

	var checkpoint CompactionCheckpoint
	if err := json.Unmarshal(checkpointRaw, &checkpoint); err != nil {
		return RunnerContext{}, fmt.Errorf("%w: decode durable checkpoint: %v", ErrInvalidCompactionCheckpoint, err)
	}
	if err := ValidateCompactionCheckpoint(checkpoint); err != nil {
		return RunnerContext{}, fmt.Errorf("load compaction checkpoint for session %q: %w", sessionID, err)
	}
	if err := validateCheckpointEpoch(result.Epoch, checkpoint); err != nil {
		return RunnerContext{}, err
	}
	result.Checkpoint = &checkpoint
	result.Messages, err = sqliteMessages(ctx, tx, sessionID, checkpoint.PreservedFromSeq-1)
	if err != nil {
		return RunnerContext{}, err
	}
	if checkpoint.AnchorUserSeq <= result.Epoch.BaselineSeq {
		anchor, err := sqliteMessageAt(ctx, tx, sessionID, checkpoint.AnchorUserSeq)
		if err != nil {
			return RunnerContext{}, err
		}
		result.Anchor = &anchor
	}
	return result, tx.Commit()
}

func validateCheckpointEpoch(current ContextEpoch, checkpoint CompactionCheckpoint) error {
	if current.BaselineSeq != checkpoint.CoveredThroughSeq {
		return fmt.Errorf("%w: corrupt checkpoint epoch baseline=%d covered=%d", ErrInvalidCompactionCheckpoint, current.BaselineSeq, checkpoint.CoveredThroughSeq)
	}
	if current.Revision != checkpoint.ExpectedEpoch.Revision+1 {
		return fmt.Errorf("%w: corrupt checkpoint epoch revision=%d expected_previous=%d", ErrInvalidCompactionCheckpoint, current.Revision, checkpoint.ExpectedEpoch.Revision)
	}
	if current.Agent != checkpoint.ExpectedEpoch.Agent {
		return fmt.Errorf("%w: corrupt checkpoint epoch agent mismatch", ErrInvalidCompactionCheckpoint)
	}
	if current.Model != checkpoint.ExpectedEpoch.Model {
		return fmt.Errorf("%w: corrupt checkpoint epoch model mismatch", ErrInvalidCompactionCheckpoint)
	}
	if current.Model != "" && checkpoint.Model != current.Model {
		return fmt.Errorf("%w: corrupt checkpoint generation model mismatch", ErrInvalidCompactionCheckpoint)
	}
	return nil
}

// CommitCompaction inserta el evento y avanza baseline/revision/checkpoint en
// una sola transaccion. El UPDATE final compara el epoch completo para que dos
// procesos con el mismo snapshot no puedan confirmar ambos checkpoints.
func (s *SQLiteStore) CommitCompaction(ctx context.Context, sessionID string, checkpoint CompactionCheckpoint) (seqResult Seq, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("SQLiteStore.CommitCompaction session %q: %w", sessionID, err)
		}
	}()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := ValidateCompactionCheckpoint(checkpoint); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if ok, err := sqliteSessionExists(ctx, tx, sessionID); err != nil {
		return 0, err
	} else if !ok {
		return 0, ErrSessionNotFound
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO session_context (session_id) VALUES (?) ON CONFLICT(session_id) DO NOTHING`, sessionID,
	); err != nil {
		return 0, err
	}

	var current ContextEpoch
	if err := tx.QueryRowContext(ctx,
		`SELECT agent, model, baseline_seq, revision FROM session_context WHERE session_id = ?`, sessionID,
	).Scan(&current.Agent, &current.Model, &current.BaselineSeq, &current.Revision); err != nil {
		return 0, err
	}
	if current != checkpoint.ExpectedEpoch || checkpoint.CoveredThroughSeq < current.BaselineSeq {
		return 0, compactionConflict("epoch mismatch: expected=%+v current=%+v covered=%d", checkpoint.ExpectedEpoch, current, checkpoint.CoveredThroughSeq)
	}

	var lastSeq Seq
	if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM events WHERE session_id = ?`, sessionID).Scan(&lastSeq); err != nil {
		return 0, err
	}
	events, err := sqliteEventsForValidation(ctx, tx, sessionID, checkpoint.AnchorUserSeq)
	if err != nil {
		return 0, err
	}
	if checkpoint.CoveredThroughSeq > lastSeq {
		return 0, compactionConflict("covered sequence out of range: covered=%d last=%d", checkpoint.CoveredThroughSeq, lastSeq)
	}
	if !validCompactionReferences(events, checkpoint) {
		return 0, compactionConflict("invalid references: covered=%d anchor=%d preserved=%d", checkpoint.CoveredThroughSeq, checkpoint.AnchorUserSeq, checkpoint.PreservedFromSeq)
	}

	checkpointRaw, err := json.Marshal(checkpoint)
	if err != nil {
		return 0, err
	}
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO events (session_id, seq, kind, has_message, compaction)
		 VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM events WHERE session_id = ?), ?, 0, ?)
		 RETURNING seq`,
		sessionID, sessionID, string(KindContextCompacted), checkpointRaw,
	).Scan(&seq); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE session_context
		    SET baseline_seq = ?, revision = revision + 1, checkpoint = ?
		  WHERE session_id = ? AND agent = ? AND model = ? AND baseline_seq = ? AND revision = ?`,
		checkpoint.CoveredThroughSeq, checkpointRaw, sessionID,
		checkpoint.ExpectedEpoch.Agent, checkpoint.ExpectedEpoch.Model,
		checkpoint.ExpectedEpoch.BaselineSeq, checkpoint.ExpectedEpoch.Revision,
	)
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if rows != 1 {
		return 0, compactionConflict("compare-and-swap failed: expected=%+v current changed", checkpoint.ExpectedEpoch)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return Seq(seq), nil
}

// PendingToolCalls reconstruye la proyeccion durable de Tool.Called sin
// Tool.Success/Tool.Failed posterior, en orden de llamada. ErrSessionNotFound si
// la sesion no existe. Aplica el mismo fold que MemoryStore sobre el log ordenado.
func (s *SQLiteStore) PendingToolCalls(ctx context.Context, sessionID string) ([]PendingTool, error) {
	ok, err := s.exists(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrSessionNotFound
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT kind, call_id, tool_name
		   FROM events
		  WHERE session_id = ?
		  ORDER BY seq`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pending := make(map[string]PendingTool)
	order := make([]string, 0)
	for rows.Next() {
		var kind, callID, toolName string
		if err := rows.Scan(&kind, &callID, &toolName); err != nil {
			return nil, err
		}
		switch EventKind(kind) {
		case KindToolCalled:
			if _, ok := pending[callID]; !ok {
				order = append(order, callID)
			}
			pending[callID] = PendingTool{CallID: callID, ToolName: toolName}
		case KindToolSuccess, KindToolFailed:
			delete(pending, callID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]PendingTool, 0, len(pending))
	for _, callID := range order {
		if tool, ok := pending[callID]; ok {
			out = append(out, tool)
		}
	}
	return out, nil
}

// DeleteSession borra todos los eventos durables de la sesion. ErrSessionNotFound
// si la sesion no existe. Las demas sesiones quedan intactas. El mutex serializa
// el chequeo de existencia y el DELETE, igual que AppendEvent.
func (s *SQLiteStore) DeleteSession(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ok, err := s.exists(ctx, sessionID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrSessionNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_context WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

type sqliteQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func sqliteSessionExists(ctx context.Context, queryer sqliteQueryer, sessionID string) (bool, error) {
	var one int
	err := queryer.QueryRowContext(ctx, `SELECT 1 FROM events WHERE session_id = ? LIMIT 1`, sessionID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func sqliteMessages(ctx context.Context, queryer sqliteQueryer, sessionID string, sinceSeq Seq) ([]Message, error) {
	rows, err := queryer.QueryContext(ctx,
		`SELECT msg_id, role, text, seq, tool_calls, tool_call_id
		   FROM events WHERE session_id = ? AND has_message = 1 AND seq > ? ORDER BY seq`,
		sessionID, sinceSeq,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Message, 0)
	for rows.Next() {
		var message Message
		var role string
		var toolCalls []byte
		var toolCallID sql.NullString
		if err := rows.Scan(&message.ID, &role, &message.Text, &message.Seq, &toolCalls, &toolCallID); err != nil {
			return nil, err
		}
		message.Role = Role(role)
		message.ToolCallID = toolCallID.String
		if len(toolCalls) > 0 {
			if err := json.Unmarshal(toolCalls, &message.ToolCalls); err != nil {
				return nil, err
			}
		}
		out = append(out, message)
	}
	return out, rows.Err()
}

func sqliteMessageAt(ctx context.Context, queryer sqliteQueryer, sessionID string, seq Seq) (Message, error) {
	var message Message
	var role string
	var toolCalls []byte
	var toolCallID sql.NullString
	err := queryer.QueryRowContext(ctx,
		`SELECT msg_id, role, text, seq, tool_calls, tool_call_id
		   FROM events WHERE session_id = ? AND seq = ? AND has_message = 1`,
		sessionID, seq,
	).Scan(&message.ID, &role, &message.Text, &message.Seq, &toolCalls, &toolCallID)
	if err != nil {
		return Message{}, err
	}
	message.Role = Role(role)
	message.ToolCallID = toolCallID.String
	if len(toolCalls) > 0 {
		if err := json.Unmarshal(toolCalls, &message.ToolCalls); err != nil {
			return Message{}, err
		}
	}
	return message, nil
}

func sqliteEventsForValidation(ctx context.Context, queryer sqliteQueryer, sessionID string, fromSeq Seq) ([]SessionEvent, error) {
	rows, err := queryer.QueryContext(ctx,
		`SELECT seq, has_message, msg_id, role, text, tool_calls, tool_call_id
		   FROM events WHERE session_id = ? AND has_message = 1 AND seq >= ? ORDER BY seq`, sessionID, fromSeq,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]SessionEvent, 0)
	for rows.Next() {
		var event SessionEvent
		var hasMessage int
		var msgID, role, text, toolCallID sql.NullString
		var toolCalls []byte
		if err := rows.Scan(&event.Seq, &hasMessage, &msgID, &role, &text, &toolCalls, &toolCallID); err != nil {
			return nil, err
		}
		message := Message{ID: msgID.String, Role: Role(role.String), Text: text.String, ToolCallID: toolCallID.String}
		if len(toolCalls) > 0 {
			if err := json.Unmarshal(toolCalls, &message.ToolCalls); err != nil {
				return nil, err
			}
		}
		event.Message = &message
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

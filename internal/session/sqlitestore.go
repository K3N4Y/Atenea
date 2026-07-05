package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
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
  PRIMARY KEY (session_id, seq)
);`

// SQLiteStore implementa Store sobre SQLite via el driver puro Go modernc.org/sqlite.
type SQLiteStore struct {
	db *sql.DB
	mu sync.Mutex
}

var _ Store = (*SQLiteStore)(nil)

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
	if dsn != ":memory:" && !strings.HasPrefix(dsn, "file:") {
		dsn = "file:" + dsn + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Obligatorio: con ":memory:" cada conexion del pool tendria su PROPIA base y
	// se perderian datos entre llamadas. Ademas serializa el unico escritor.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	// Migracion idempotente para bases ya creadas: CREATE TABLE IF NOT EXISTS no
	// agrega columnas, asi que las anadimos con ALTER ignorando solo el error de
	// columna duplicada (la base ya tenia la columna).
	for _, stmt := range []string{
		`ALTER TABLE events ADD COLUMN tool_calls BLOB`,
		`ALTER TABLE events ADD COLUMN tool_call_id TEXT`,
		`ALTER TABLE events ADD COLUMN ev_text TEXT`,
		`ALTER TABLE events ADD COLUMN diff TEXT`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			db.Close()
			return nil, err
		}
	}
	return &SQLiteStore{db: db}, nil
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
	s.mu.Lock()
	defer s.mu.Unlock()

	hasMessage := 0
	var msgID, role, text, toolCallID sql.NullString
	var toolCalls []byte
	if ev.Message != nil {
		hasMessage = 1
		msgID = sql.NullString{String: ev.Message.ID, Valid: true}
		role = sql.NullString{String: string(ev.Message.Role), Valid: true}
		text = sql.NullString{String: ev.Message.Text, Valid: true}
		toolCallID = sql.NullString{String: ev.Message.ToolCallID, Valid: true}
		if len(ev.Message.ToolCalls) > 0 {
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

	// ev_text guarda el Text top-level del SessionEvent (Reasoning/Text.*,
	// Tool.Input.Delta), independiente de la columna text (que es Message.Text).
	// Sin esto la rehidratacion pierde el razonamiento y la respuesta del asistente,
	// que viajan en ev.Text y no en un Message.
	var seq int64
	if err := s.db.QueryRowContext(ctx,
		`INSERT INTO events
		   (session_id, seq, kind, has_message, msg_id, role, text, call_id, tool_name, input, usage, error, tool_calls, tool_call_id, ev_text, diff)
		 VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM events WHERE session_id = ?), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 RETURNING seq`,
		sessionID, sessionID, string(ev.Kind), hasMessage, msgID, role, text,
		ev.CallID, ev.ToolName, []byte(ev.Input), usage, ev.Error, toolCalls, toolCallID, ev.Text, ev.Diff,
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
		        input, usage, error, tool_calls, tool_call_id, ev_text, diff
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
			input, usage, toolCalls                   []byte
		)
		if err := rows.Scan(&seq, &kind, &hasMessage, &msgID, &role, &text,
			&callID, &toolName, &input, &usage, &errText, &toolCalls, &tcID, &evText, &diff); err != nil {
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
	return ContextEpoch{}, nil
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
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM events WHERE session_id = ?`, sessionID,
	); err != nil {
		return err
	}
	return nil
}

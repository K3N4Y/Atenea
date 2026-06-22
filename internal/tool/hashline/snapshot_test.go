package hashline

import (
	"fmt"
	"sync"
	"testing"
)

// TestMemSnapshotStore_RecordThenHead afirma que Record graba la version y
// devuelve el tag (== ComputeFileHash del texto), y que Head devuelve esa version
// con el texto y el hash correctos.
func TestMemSnapshotStore_RecordThenHead(t *testing.T) {
	s := NewMemSnapshotStore()
	tag := s.Record("/abs/foo.go", "a\nb\n")

	if want := ComputeFileHash("a\nb\n"); tag != want {
		t.Fatalf("Record: se esperaba tag %q, se obtuvo %q", want, tag)
	}

	snap := s.Head("/abs/foo.go")
	if snap == nil {
		t.Fatalf("Head: se esperaba un snapshot, se obtuvo nil")
	}
	if snap.Text != "a\nb\n" {
		t.Fatalf("Head: se esperaba Text %q, se obtuvo %q", "a\nb\n", snap.Text)
	}
	if snap.Hash != tag {
		t.Fatalf("Head: se esperaba Hash %q, se obtuvo %q", tag, snap.Hash)
	}
}

// TestMemSnapshotStore_RecordSeenLines afirma que RecordSeenLines marca las lineas
// vistas en el Head: el edit rechazara anclas a lineas fuera de este set.
func TestMemSnapshotStore_RecordSeenLines(t *testing.T) {
	s := NewMemSnapshotStore()
	tag := s.Record("/abs/foo.go", "a\nb\n")
	s.RecordSeenLines("/abs/foo.go", tag, []int{1, 2})

	snap := s.Head("/abs/foo.go")
	if snap == nil {
		t.Fatalf("Head: se esperaba un snapshot, se obtuvo nil")
	}
	if _, ok := snap.Seen[1]; !ok {
		t.Fatalf("RecordSeenLines: se esperaba la clave 1 en Seen, se obtuvo %v", snap.Seen)
	}
	if _, ok := snap.Seen[2]; !ok {
		t.Fatalf("RecordSeenLines: se esperaba la clave 2 en Seen, se obtuvo %v", snap.Seen)
	}
}

// TestMemSnapshotStore_RecordIdenticalReusesTag afirma la propiedad de
// read-fusion: grabar el mismo texto dos veces devuelve el mismo tag, y ese tag
// coincide con ComputeFileHash del texto. Asi dos lecturas de bytes identicos dan
// el mismo header [path#HASH] y los edits encadenan sin invalidarse.
func TestMemSnapshotStore_RecordIdenticalReusesTag(t *testing.T) {
	s := NewMemSnapshotStore()
	first := s.Record("/abs/foo.go", "a\nb\n")
	second := s.Record("/abs/foo.go", "a\nb\n")

	if first != second {
		t.Fatalf("Record: se esperaba el mismo tag al grabar texto identico, se obtuvo %q vs %q", first, second)
	}
	if want := ComputeFileHash("a\nb\n"); first != want {
		t.Fatalf("Record: se esperaba tag %q (== ComputeFileHash), se obtuvo %q", want, first)
	}
}

// TestMemSnapshotStore_ByHashFindsRecordedVersion afirma que el historial se
// retiene: tras grabar dos versiones distintas del mismo path, ByHash con el hash
// viejo devuelve la version vieja (aunque ya no sea el Head), con el nuevo la
// nueva, y con un hash inexistente devuelve nil. El recovery del edit depende de
// poder recuperar una version anterior por su hash.
func TestMemSnapshotStore_ByHashFindsRecordedVersion(t *testing.T) {
	s := NewMemSnapshotStore()
	oldHash := s.Record("/abs/foo.go", "a\nb\n")
	newHash := s.Record("/abs/foo.go", "a\nB\n")

	if oldHash == newHash {
		t.Fatalf("setup: se esperaban hashes distintos para textos distintos, ambos %q", oldHash)
	}

	old := s.ByHash("/abs/foo.go", oldHash)
	if old == nil {
		t.Fatalf("ByHash(viejo): se esperaba la version vieja, se obtuvo nil")
	}
	if old.Text != "a\nb\n" {
		t.Fatalf("ByHash(viejo): se esperaba Text %q, se obtuvo %q", "a\nb\n", old.Text)
	}

	current := s.ByHash("/abs/foo.go", newHash)
	if current == nil {
		t.Fatalf("ByHash(nuevo): se esperaba la version nueva, se obtuvo nil")
	}
	if current.Text != "a\nB\n" {
		t.Fatalf("ByHash(nuevo): se esperaba Text %q, se obtuvo %q", "a\nB\n", current.Text)
	}

	if missing := s.ByHash("/abs/foo.go", "ZZZZ"); missing != nil {
		t.Fatalf("ByHash(inexistente): se esperaba nil, se obtuvo %+v", missing)
	}
}

// TestMemSnapshotStore_ConcurrentRecord afirma que Record es seguro bajo uso
// concurrente: el runner asienta tools en paralelo. Lanza goroutines que graban
// sobre el mismo path y sobre paths distintos; debe correr limpio bajo -race.
func TestMemSnapshotStore_ConcurrentRecord(t *testing.T) {
	s := NewMemSnapshotStore()
	const goroutines = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			// Mitad sobre el mismo path (contienden por el mismo historial),
			// mitad sobre paths distintos (crean entradas en paralelo).
			if i%2 == 0 {
				s.Record("/abs/shared.go", fmt.Sprintf("contenido %d\n", i))
			} else {
				s.Record(fmt.Sprintf("/abs/file_%d.go", i), "contenido\n")
			}
		}(i)
	}
	wg.Wait()

	if s.Head("/abs/shared.go") == nil {
		t.Fatalf("Head: se esperaba un snapshot para el path compartido tras los Record concurrentes")
	}
}

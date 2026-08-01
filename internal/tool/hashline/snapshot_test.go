package hashline

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestMemSnapshotStore_RecordThenHead afirma que Record graba la version y
// devuelve el tag (== ComputeFileHash del texto), y que Head devuelve esa version
// con el texto y el hash correctos.
func TestMemSnapshotStore_RecordThenHead(t *testing.T) {
	s := NewMemSnapshotStore()
	tag, ok := s.Record("/abs/foo.go", "a\nb\n")
	if !ok {
		t.Fatal("Record unexpectedly rejected")
	}

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
	tag, _ := s.Record("/abs/foo.go", "a\nb\n")
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
	first, _ := s.Record("/abs/foo.go", "a\nb\n")
	second, _ := s.Record("/abs/foo.go", "a\nb\n")

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
	oldHash, _ := s.Record("/abs/foo.go", "a\nb\n")
	newHash, _ := s.Record("/abs/foo.go", "a\nB\n")

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

func TestMemSnapshotStore_RecordRetainsNewestWhenSamePathHistoryExceedsByteLimit(t *testing.T) {
	s := NewMemSnapshotStore()
	const versionSize = defaultMaxBytes/4 + 1
	path := "/abs/large.go"

	var newestHash string
	for i := 0; i < defaultMaxVersions; i++ {
		text := strings.Repeat(string(rune('a'+i)), versionSize)
		hash, recorded := s.Record(path, text)
		if !recorded {
			t.Fatalf("Record version %d unexpectedly rejected", i)
		}
		newestHash = hash
	}

	if newest := s.ByHash(path, newestHash); newest == nil {
		t.Fatal("Record reported success after evicting the newly recorded snapshot")
	}
	if head := s.Head(path); head == nil || head.Hash != newestHash {
		t.Fatalf("Head = %+v, want newest hash %q", head, newestHash)
	}
	if s.bytes > defaultMaxBytes {
		t.Fatalf("retained %d bytes, limit is %d", s.bytes, defaultMaxBytes)
	}
}

func TestMemSnapshotStore_RecordRejectsAmbiguousHashWithoutMutatingHistory(t *testing.T) {
	s := NewMemSnapshotStore()
	const path = "/abs/collision.go"
	firstText, secondText := "collision-88", "collision-640"
	if firstHash, secondHash := ComputeFileHash(firstText), ComputeFileHash(secondText); firstHash != secondHash {
		t.Fatalf("collision fixture drifted: %q != %q", firstHash, secondHash)
	}

	hash, recorded := s.Record(path, firstText)
	if !recorded {
		t.Fatal("first colliding snapshot unexpectedly rejected")
	}
	if secondHash, secondRecorded := s.Record(path, secondText); secondRecorded || secondHash != "" {
		t.Fatalf("ambiguous snapshot returned hash=%q recorded=%v", secondHash, secondRecorded)
	}

	snapshot := s.ByHash(path, hash)
	if snapshot == nil || snapshot.Text != firstText {
		t.Fatalf("rejected collision mutated prior snapshot: %+v", snapshot)
	}
	if head := s.Head(path); head == nil || head.Text != firstText {
		t.Fatalf("rejected collision changed head: %+v", head)
	}
}

// TestMemSnapshotStore_ByHashReturnsDefensiveSeenCopy afirma que ByHash devuelve
// una copia de Seen, no el map vivo: una escritura posterior via RecordSeenLines no
// debe aparecer en el snapshot devuelto antes. Asi el Patcher puede iterar Seen
// fuera del mutex sin compartir el map con RecordSeenLines (evita el data race).
func TestMemSnapshotStore_ByHashReturnsDefensiveSeenCopy(t *testing.T) {
	s := NewMemSnapshotStore()
	hash, _ := s.Record("/abs/foo.go", "a\nb\nc\n")
	s.RecordSeenLines("/abs/foo.go", hash, []int{1})

	snap := s.ByHash("/abs/foo.go", hash)
	if snap == nil {
		t.Fatalf("ByHash: se esperaba un snapshot, se obtuvo nil")
	}

	// Mutacion posterior del store: no debe filtrarse a la copia ya entregada.
	s.RecordSeenLines("/abs/foo.go", hash, []int{2})
	if _, leaked := snap.Seen[2]; leaked {
		t.Fatalf("ByHash: la copia comparte el map vivo; la linea 2 se filtro a un snapshot previo: %v", snap.Seen)
	}
	if _, ok := snap.Seen[1]; !ok {
		t.Fatalf("ByHash: la copia debio conservar la linea 1 vista al momento de la copia: %v", snap.Seen)
	}

	// Y el store SI ve la linea 2 en una nueva consulta.
	fresh := s.ByHash("/abs/foo.go", hash)
	if _, ok := fresh.Seen[2]; !ok {
		t.Fatalf("ByHash: una nueva copia debio incluir la linea 2 grabada despues: %v", fresh.Seen)
	}
}

// TestMemSnapshotStore_HeadReturnsDefensiveSeenCopy afirma la misma propiedad para
// Head: el caso de borde es que mutar el Seen del snapshot devuelto no afecte al
// store, y viceversa.
func TestMemSnapshotStore_HeadReturnsDefensiveSeenCopy(t *testing.T) {
	s := NewMemSnapshotStore()
	hash, _ := s.Record("/abs/foo.go", "a\nb\n")
	s.RecordSeenLines("/abs/foo.go", hash, []int{1})

	snap := s.Head("/abs/foo.go")
	if snap == nil {
		t.Fatalf("Head: se esperaba un snapshot, se obtuvo nil")
	}

	// Mutar la copia no debe contaminar el store.
	snap.Seen[99] = struct{}{}
	if again := s.Head("/abs/foo.go"); again != nil {
		if _, leaked := again.Seen[99]; leaked {
			t.Fatalf("Head: mutar la copia contamino el store: %v", again.Seen)
		}
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

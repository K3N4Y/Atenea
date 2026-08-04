package session

import (
	"context"
	"testing"
)

// TestMemoryInbox_AdmitHasPendingPromote fija el contrato del inbox que el loop
// (Run) asume: queue es FIFO y Promote saca UNO por llamada; steer se drena
// entero en una sola Promote; y promover una entrega vacia devuelve nil. Es el
// caso aislado del inbox: sin store, sin runner, solo el contrato Admit ->
// HasPending -> Promote.
func TestMemoryInbox_AdmitHasPendingPromote(t *testing.T) {
	ctx := context.Background()
	inbox := NewMemoryInbox()

	// Admitir un queue lo deja pendiente.
	if err := inbox.Admit(ctx, "s1", Prompt{Text: "p1"}, DeliveryQueue); err != nil {
		t.Fatalf("Admit (queue) error inesperado: %v", err)
	}
	has, err := inbox.HasPending(ctx, "s1", DeliveryQueue)
	if err != nil {
		t.Fatalf("HasPending (queue) error inesperado: %v", err)
	}
	if !has {
		t.Errorf("HasPending(queue) = false tras Admit, quiero true")
	}

	// Promover queue saca el prompt y lo deja sin pendientes (uno por promote).
	got, err := inbox.Promote(ctx, "s1", DeliveryQueue)
	if err != nil {
		t.Fatalf("Promote (queue) error inesperado: %v", err)
	}
	if len(got) != 1 || got[0].Text != "p1" {
		t.Fatalf("Promote(queue) = %+v, quiero [{p1}]", got)
	}
	has, err = inbox.HasPending(ctx, "s1", DeliveryQueue)
	if err != nil {
		t.Fatalf("HasPending (queue) error inesperado: %v", err)
	}
	if has {
		t.Errorf("HasPending(queue) = true tras Promote, quiero false (drenado)")
	}

	// Dos steers admitidos se drenan en una sola Promote, en orden de admision.
	if err := inbox.Admit(ctx, "s1", Prompt{Text: "s1text"}, DeliverySteer); err != nil {
		t.Fatalf("Admit (steer s1) error inesperado: %v", err)
	}
	if err := inbox.Admit(ctx, "s1", Prompt{Text: "s2text"}, DeliverySteer); err != nil {
		t.Fatalf("Admit (steer s2) error inesperado: %v", err)
	}
	steers, err := inbox.Promote(ctx, "s1", DeliverySteer)
	if err != nil {
		t.Fatalf("Promote (steer) error inesperado: %v", err)
	}
	if len(steers) != 2 || steers[0].Text != "s1text" || steers[1].Text != "s2text" {
		t.Fatalf("Promote(steer) = %+v, quiero [{s1text} {s2text}] (drena ambos en orden)", steers)
	}
	has, err = inbox.HasPending(ctx, "s1", DeliverySteer)
	if err != nil {
		t.Fatalf("HasPending (steer) error inesperado: %v", err)
	}
	if has {
		t.Errorf("HasPending(steer) = true tras Promote, quiero false (drenado)")
	}

	// Promover una entrega vacia devuelve nil (len 0) sin error.
	empty, err := inbox.Promote(ctx, "s1", DeliveryQueue)
	if err != nil {
		t.Fatalf("Promote (queue vacio) error inesperado: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("Promote(queue vacio) = %+v, quiero nil (len 0)", empty)
	}
}

func TestMemoryInbox_DefensivelyCopiesImageData(t *testing.T) {
	ctx := context.Background()
	inbox := NewMemoryInbox()
	data := []byte{1, 2, 3}
	prompt := Prompt{Images: []Image{{MediaType: "image/png", Data: data}}}
	if err := inbox.Admit(ctx, "s1", prompt, DeliveryQueue); err != nil {
		t.Fatal(err)
	}
	if err := inbox.Admit(ctx, "s1", Prompt{Images: []Image{{MediaType: "image/png", Data: []byte{4, 5, 6}}}}, DeliveryQueue); err != nil {
		t.Fatal(err)
	}
	data[0] = 9
	prompt.Images[0].Data[1] = 9

	got, err := inbox.Promote(ctx, "s1", DeliveryQueue)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || string(got[0].Images[0].Data) != string([]byte{1, 2, 3}) {
		t.Fatalf("Promote image = %+v, want original bytes", got)
	}
	got[0].Images[0].Data[2] = 9
	next, err := inbox.Promote(ctx, "s1", DeliveryQueue)
	if err != nil || len(next) != 1 || string(next[0].Images[0].Data) != string([]byte{4, 5, 6}) {
		t.Fatalf("second Promote = (%+v, %v), want independent image bytes", next, err)
	}
}

package tui

import (
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// Smooth streaming del texto que llega por deltas (assistant y pensamiento;
// paridad con el escritorio, frontend/src/lib/reveal.ts): desacopla el ritmo
// de red del ritmo de lectura. Los deltas se acumulan completos en entry.text
// pero la vista revela solo un prefijo (entry.revealed runas) que avanza con
// cada tick, para que el texto se "escriba" suave en vez de aparecer a saltos.

// revealTickMsg es el tick del loop de reveal del smooth streaming: cada tick
// avanza el texto que la vista revela progresivamente (analogo al
// spinner.TickMsg del indicador de trabajo).
type revealTickMsg struct{}

const (
	// revealTickInterval es el periodo del loop de reveal (~30 fps): suave al
	// ojo sin rerender por cada delta de red.
	revealTickInterval = 33 * time.Millisecond

	// revealMSPerRune es el ritmo base de lectura: ~5ms por runa (~200 rps),
	// el mismo del escritorio (MS_PER_CHAR en reveal.ts).
	revealMSPerRune = 5

	// revealCatchUpFrames acota el retraso: ante un backlog grande se acelera
	// para drenarlo en a lo sumo este numero de ticks, asi el texto visible
	// nunca queda muchos ticks atras del backend (con modelos rapidos evita
	// quedarse segundos por detras).
	revealCatchUpFrames = 8
)

// revealBaseRunes es cuantas runas revela un tick a ritmo base: el intervalo
// del tick repartido al ritmo por runa, redondeado hacia arriba (~7 runas).
const revealBaseRunes = (int(revealTickInterval/time.Millisecond) + revealMSPerRune - 1) / revealMSPerRune

// revealStep devuelve cuantas runas revelar en este tick: el ritmo base o el
// catch-up proporcional al backlog (el que sea mayor, con el catch-up
// redondeado hacia arriba), acotado a [1, remaining].
func revealStep(remaining int) int {
	if remaining <= 0 {
		return 0
	}
	catchUp := (remaining + revealCatchUpFrames - 1) / revealCatchUpFrames
	return min(max(revealBaseRunes, catchUp), remaining)
}

// revealTick agenda el proximo tick del loop de reveal.
func revealTick() tea.Cmd {
	return tea.Tick(revealTickInterval, func(time.Time) tea.Msg {
		return revealTickMsg{}
	})
}

// backlog devuelve cuantas runas del texto de la entrada faltan por revelar.
// Solo participan las entradas cuyo texto llega por streaming (assistant y
// pensamiento); el resto de entradas se muestra completo desde que existe.
func (e entry) backlog() int {
	if e.kind != entryAssistant && e.kind != entryReasoning {
		return 0
	}
	return max(utf8.RuneCountInString(e.text)-e.revealed, 0)
}

// settled indica que el bloque llego a su forma final: el streaming cerro
// (live apagado) y el reveal dreno todo el backlog. Solo entonces la vista
// cambia de forma (markdown en el assistant, resumen colapsado en el
// pensamiento): saltar antes flashearia de golpe el texto a medio animar.
func (e entry) settled() bool {
	return !e.live && e.backlog() == 0
}

// revealedText devuelve el prefijo ya revelado del texto de la entrada. El
// corte es POR RUNAS, nunca por bytes: un caracter multibyte jamas se parte
// a la mitad.
func (e entry) revealedText() string {
	runes := []rune(e.text)
	if e.revealed >= len(runes) {
		return e.text
	}
	return string(runes[:e.revealed])
}

// hasBacklog indica si alguna entrada tiene texto sin revelar.
func (m Model) hasBacklog() bool {
	for _, e := range m.entries {
		if e.backlog() > 0 {
			return true
		}
	}
	return false
}

// advanceReveal avanza un paso de tick el reveal de cada entrada con backlog.
func (m Model) advanceReveal() Model {
	for i := range m.entries {
		if b := m.entries[i].backlog(); b > 0 {
			m.entries[i].revealed += revealStep(b)
		}
	}
	return m
}

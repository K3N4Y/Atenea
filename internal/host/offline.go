package host

import "github.com/K3N4Y/atenea/internal/llm"

// OfflineProviderID names the fake a host lands on when there is no credential
// anywhere — not in the environment, not in the credentials file. It is in no
// catalog, so nothing can ever select it: a host only ever lands on it. Both UIs
// match on this id to say that the replies are canned, instead of presenting
// them as a model's.
const OfflineProviderID = "demo"

// offlineSnapshot is that fake presented as a provider like any other, so the
// model picker and the composer footer have something honest to show rather than
// a blank where a selection goes.
func offlineSnapshot() llm.ProviderSnapshot {
	return llm.ProviderSnapshot{
		ProviderID:   OfflineProviderID,
		ProviderName: "Demo",
		BaseURL:      "demo://local",
		Model:        "demo",
		Provider:     demoProvider(),
	}
}

// demoProvider scripts one short turn — text, then Step.Ended — so `wails dev`
// and a terminal with no key both show real streaming without a network.
func demoProvider() llm.Provider {
	return llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "Hello from atenea."},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
}

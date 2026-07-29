package providerconfig

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DeviceLoginFunc starts a device-code login for one provider.
//
// It is split from the waiting on purpose. A host has to paint the code before
// anything blocks — a user staring at a spinner has nothing to type — and the two
// halves have different lifetimes: starting is bounded by the click that triggered
// it, waiting by a human walking to their browser.
type DeviceLoginFunc func(ctx context.Context, def Provider) (DeviceCode, error)

// DeviceCode is one pending login: what to show the user, and the wait that turns
// their approval into a credential.
type DeviceCode struct {
	// UserCode is what the user types at VerificationURI.
	UserCode string
	// VerificationURI is the page to type it into.
	VerificationURI string
	// ExpiresAt is when the code stops being approvable, so a host can say how long
	// is left instead of leaving a dead code on screen.
	ExpiresAt time.Time
	// Await blocks until the user approves the code, ctx is cancelled, or the code
	// expires. It is a closure rather than a method because what it waits on is the
	// authorization server's own handle for this login — the format's business, and
	// nobody else's.
	Await func(ctx context.Context) (OAuthCredential, error)
}

// DeviceLogin is a login in flight, as a host holds it: the code to show, plus the
// two things a host can do about it.
type DeviceLogin struct {
	ProviderID      string
	ProviderName    string
	UserCode        string
	VerificationURI string
	ExpiresAt       time.Time
	// Attempt identifies THIS attempt among the ones a provider has had. A host
	// that retires a login it started passes it back through
	// [Service.CancelDeviceLoginAttempt], so an attempt the user walked away from
	// cannot cancel the code that replaced it — which is the one on screen.
	Attempt uint64
}

// pendingLogin is one login the service is running. The polling lives in a
// goroutine of its own with a context of its own, so awaiting it is cancellable
// without cancelling it: a TUI that redraws, or a Wails frontend that reloads,
// stops waiting without throwing away a code the user is in the middle of typing.
type pendingLogin struct {
	login DeviceLogin
	// seq is the attempt's start order, taken before the mint goes out. Retirement
	// is decided on it rather than on arrival order, because arriving second does
	// not mean starting second.
	seq    uint64
	cancel context.CancelFunc
	// settled closes once result is written, which is how every waiter reads the
	// same outcome. A one-shot channel would hand the result to whoever got there
	// first and leave the rest blocked until the process ends — and two waiters on
	// one login is not exotic: a retry that replaces the code puts both hosts'
	// pending waits on whichever attempt was installed last.
	settled chan struct{}
	result  loginResult
}

// settle records the outcome and releases every waiter. Only the polling
// goroutine calls it, exactly once, so the write is ordered before any read by the
// close.
func (p *pendingLogin) settle(result loginResult) {
	p.result = result
	close(p.settled)
}

type loginResult struct {
	active Active
	err    error
}

// ErrNoPendingLogin is what awaiting or cancelling a login nobody started reports.
// A host may act on a list an earlier cancellation already invalidated, so this is
// a condition to recognize rather than a message to show.
var ErrNoPendingLogin = errors.New("no login is pending for this provider")

// ErrLoginCancelled is what awaiting a login reports once the login was abandoned:
// by [Service.CancelDeviceLogin], or by a retry that retired its code.
//
// It lives here rather than in either host because both need the same answer and
// neither may guess it from a message. Cancelling is a decision the user took, so a
// host that painted "context canceled" in red would be reporting the button they
// just clicked as a failure.
var ErrLoginCancelled = errors.New("the login was cancelled")

// StartDeviceLogin begins an OAuth device-code login and returns the code to show
// the user. Nothing blocks on the human: the polling runs in the background until
// [Service.AwaitDeviceLogin] collects it or [Service.CancelDeviceLogin] stops it.
//
// It lives here, in the service both hosts share, rather than in either UI. The
// flow has to store a credential, rotate a refresh token and activate a selection
// — three things that are the same in a terminal and in a window — so a host that
// re-implemented the orchestration would be re-implementing exactly the parts that
// must not differ.
//
// ctx bounds the first request only, which is the one that mints the code.
func (s *Service) StartDeviceLogin(ctx context.Context, providerID string) (DeviceLogin, error) {
	s.mu.RLock()
	provider, configured := findProvider(s.config, providerID)
	flow, isLogin := s.registry.OAuth(provider.Type)
	s.mu.RUnlock()
	if !configured {
		return DeviceLogin{}, fmt.Errorf("provider %q is not configured", providerID)
	}
	if !isLogin || flow.Login == nil {
		return DeviceLogin{}, fmt.Errorf("provider %q connects with an API key, not with a login", providerID)
	}
	if s.credentials == nil {
		return DeviceLogin{}, errors.New("credential storage is unavailable")
	}

	// The number is taken here, before the mint, so it records when this attempt
	// STARTED. Taking it on the way back would number the attempts by who answered
	// first, which is exactly the order that cannot be trusted.
	seq := s.nextLoginSeq()
	code, err := flow.Login(ctx, provider)
	if err != nil {
		return DeviceLogin{}, err
	}
	if code.UserCode == "" || code.Await == nil {
		return DeviceLogin{}, fmt.Errorf("provider %q started a login with no code to approve", providerID)
	}
	login := DeviceLogin{
		ProviderID:      providerID,
		ProviderName:    provider.Name,
		UserCode:        code.UserCode,
		VerificationURI: code.VerificationURI,
		ExpiresAt:       code.ExpiresAt,
		Attempt:         seq,
	}

	// The polling outlives the call that started it, so it gets a context that is
	// not the caller's. Cancelling it is CancelDeviceLogin's job.
	pollCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	pending := &pendingLogin{login: login, seq: seq, cancel: cancel, settled: make(chan struct{})}
	s.replacePendingLogin(providerID, pending)
	go func() {
		credential, err := code.Await(pollCtx)
		if err != nil {
			pending.settle(loginResult{active: s.Active(), err: err})
			return
		}
		active, err := s.storeLogin(pollCtx, providerID, credential)
		pending.settle(loginResult{active: active, err: err})
	}()
	return login, nil
}

// AwaitDeviceLogin waits for the pending login of a provider to complete and
// reports the resulting selection.
//
// Returning because ctx was cancelled does NOT stop the login: the credential the
// user is about to approve is worth more than the wait, and a host that really
// wants to abandon it calls CancelDeviceLogin. The login is only forgotten once it
// has actually finished.
//
// A login abandoned underneath a waiter comes back as [ErrLoginCancelled], not as
// a bare cancellation a host would have to tell apart from its own.
//
// Every waiter of one login collects the same outcome, so a host that dispatched
// two waits — a retry, a reload, two windows — never leaves one of them blocked on
// a result another already took.
func (s *Service) AwaitDeviceLogin(ctx context.Context, providerID string) (Active, error) {
	pending, ok := s.pendingLogin(providerID)
	if !ok {
		return s.Active(), ErrNoPendingLogin
	}
	select {
	case <-ctx.Done():
		return s.Active(), ctx.Err()
	case <-pending.settled:
		result := pending.result
		s.forgetPendingLogin(providerID, pending)
		// The polling context is this service's own, and the only things that cancel
		// it are CancelDeviceLogin and a retry replacing the code. Either way the
		// login was abandoned on purpose, which is a condition and not a failure.
		if errors.Is(result.err, context.Canceled) {
			return result.active, ErrLoginCancelled
		}
		return result.active, result.err
	}
}

// PendingDeviceLogin is the login in flight for a provider, if there is one. A
// host that already showed the code reads it back from here rather than keeping a
// second copy: the code is single-use and the copies would disagree the moment a
// retry replaces it.
func (s *Service) PendingDeviceLogin(providerID string) (DeviceLogin, bool) {
	pending, ok := s.pendingLogin(providerID)
	if !ok {
		return DeviceLogin{}, false
	}
	return pending.login, true
}

// CancelDeviceLogin stops whatever login is pending for a provider and forgets
// it. Cancelling one that is not there is not an error: a host may be acting on a
// code that already resolved.
//
// It is the right call for a UI that shows one code and one Cancel button, because
// the code on screen IS whatever is pending. A host that retires an attempt of its
// own — one whose code it never painted — wants
// [Service.CancelDeviceLoginAttempt] instead.
func (s *Service) CancelDeviceLogin(providerID string) {
	pending, ok := s.pendingLogin(providerID)
	if !ok {
		return
	}
	pending.cancel()
	s.forgetPendingLogin(providerID, pending)
}

// CancelDeviceLoginAttempt stops one specific attempt, and does nothing when the
// provider has moved on to another one.
//
// A host retires an attempt it abandoned — a panel the user closed before the code
// ever appeared — and by the time it gets to it the user may have started a second
// login that is live on screen. Cancelling "whatever is pending" there kills the
// code the user is actually typing, and the live wait comes back reporting a
// cancellation nobody performed.
func (s *Service) CancelDeviceLoginAttempt(providerID string, attempt uint64) {
	pending, ok := s.pendingLogin(providerID)
	if !ok || pending.seq != attempt {
		return
	}
	pending.cancel()
	s.forgetPendingLogin(providerID, pending)
}

// storeLogin persists an approved login and makes it usable at once, exactly as
// Connect does for a key: with nothing selected the provider activates on its
// default model, and when it is already the active one the live delegate is
// rebuilt so the fresh credential takes effect without a restart.
func (s *Service) storeLogin(ctx context.Context, providerID string, credential OAuthCredential) (Active, error) {
	entry := Credential{Type: CredentialTypeOAuth, OAuth: &credential}
	if err := entry.Validate(); err != nil {
		return s.Active(), fmt.Errorf("provider %q: %w", providerID, err)
	}
	s.mu.Lock()
	err := s.credentials.Put(providerID, entry)
	selected := s.config.Selected
	provider, _ := findProvider(s.config, providerID)
	s.mu.Unlock()
	if err != nil {
		return s.Active(), err
	}
	switch {
	case selected.Provider == providerID:
		return s.applySelection(ctx, providerID, selected.Model)
	case selected.Provider == "":
		if len(provider.Models) > 0 {
			return s.applySelection(ctx, providerID, provider.Models[0])
		}
	}
	return s.Active(), nil
}

// replacePendingLogin installs a login, cancelling whatever was pending for the
// same provider. Starting a second login retires the first code — the server
// stopped honoring it anyway — and leaving its goroutine polling a dead code would
// leak one per retry.
//
// Retirement follows start order, not arrival order. Two mints can be in flight at
// once and the first one started can be the second one back; installing it then
// would cancel the newer login and, with it, the code the user is looking at. The
// late arrival retires itself instead: its code is the one nobody was ever shown.
func (s *Service) replacePendingLogin(providerID string, pending *pendingLogin) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if s.logins == nil {
		s.logins = map[string]*pendingLogin{}
	}
	if previous, ok := s.logins[providerID]; ok {
		if previous.seq > pending.seq {
			pending.cancel()
			return
		}
		previous.cancel()
	}
	s.logins[providerID] = pending
}

func (s *Service) nextLoginSeq() uint64 {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	s.loginSeq++
	return s.loginSeq
}

func (s *Service) pendingLogin(providerID string) (*pendingLogin, bool) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	pending, ok := s.logins[providerID]
	return pending, ok
}

// forgetPendingLogin drops the entry only if it is still the one that finished, so
// a stale collection cannot delete the login a retry just started.
func (s *Service) forgetPendingLogin(providerID string, pending *pendingLogin) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if s.logins[providerID] == pending {
		delete(s.logins, providerID)
	}
}

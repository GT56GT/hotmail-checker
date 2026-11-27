// Package bot wires the Telegram interface around the cookie checkers.
package bot

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/GT56GT/hotmail-checker/internal/checker"
	"github.com/GT56GT/hotmail-checker/internal/config"
	"github.com/GT56GT/hotmail-checker/internal/job"
	"github.com/GT56GT/hotmail-checker/internal/provider"
	"github.com/GT56GT/hotmail-checker/internal/proxy"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"
)

// pendingKind identifies what the next cookie/proxy message should do.
type pendingKind int

const (
	pendingNone pendingKind = iota
	pendingSearch
	pendingProxyAdd
)

type pending struct {
	kind    pendingKind
	term    string
	expires time.Time
}

// Bot holds shared dependencies for every handler.
type Bot struct {
	cfg      *config.Config
	checkers []provider.Checker
	ms       *checker.Checker // nil when the Microsoft provider is disabled
	pools    *proxy.Manager
	reg      *job.Registry
	client   *gotgbot.Bot
	http     *http.Client

	mu      sync.Mutex
	pending map[int64]pending
}

// New validates the token and builds the handler set.
func New(cfg *config.Config, checkers []provider.Checker, pools *proxy.Manager, reg *job.Registry) (*Bot, error) {
	client, err := gotgbot.NewBot(cfg.BotToken, &gotgbot.BotOpts{
		BotClient: &gotgbot.BaseBotClient{
			Client: http.Client{Timeout: 60 * time.Second},
		},
	})
	if err != nil {
		return nil, err
	}

	var ms *checker.Checker
	for _, c := range checkers {
		if v, ok := c.(*checker.Checker); ok {
			ms = v
		}
	}

	return &Bot{
		cfg:      cfg,
		checkers: checkers,
		ms:       ms,
		pools:    pools,
		reg:      reg,
		client:   client,
		http:     &http.Client{Timeout: 60 * time.Second},
		pending:  make(map[int64]pending),
	}, nil
}

// Run starts long polling and blocks until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	dispatcher := ext.NewDispatcher(&ext.DispatcherOpts{
		Error: func(_ *gotgbot.Bot, _ *ext.Context, err error) ext.DispatcherAction {
			slog.Error("handler error", "err", err)
			return ext.DispatcherActionNoop
		},
		MaxRoutines: ext.DefaultMaxRoutines,
	})
	updater := ext.NewUpdater(dispatcher, nil)

	dispatcher.AddHandler(handlers.NewCommand("start", b.cmdStart))
	dispatcher.AddHandler(handlers.NewCommand("help", b.cmdStart))
	dispatcher.AddHandler(handlers.NewCommand("search", b.cmdSearch))
	dispatcher.AddHandler(handlers.NewCommand("proxies", b.cmdProxies))
	dispatcher.AddHandler(handlers.NewCommand("cancel", b.cmdCancel))
	dispatcher.AddHandler(handlers.NewCommand("stats", b.cmdStats))

	dispatcher.AddHandler(handlers.NewMessage(isCookieMessage, b.onMessage))
	dispatcher.AddHandler(handlers.NewMessage(isDocumentMessage, b.onDocument))

	err := updater.StartPolling(b.client, &ext.PollingOpts{
		DropPendingUpdates: false,
		GetUpdatesOpts: &gotgbot.GetUpdatesOpts{
			Timeout: 30,
			RequestOpts: &gotgbot.RequestOpts{
				Timeout: 45 * time.Second,
			},
		},
	})
	if err != nil {
		return err
	}

	slog.Info("polling", "bot", b.client.Username, "providers", b.providerNames())

	<-ctx.Done()
	return updater.Stop()
}

func (b *Bot) providerNames() string {
	names := make([]string, 0, len(b.checkers))
	for _, c := range b.checkers {
		names = append(names, c.Name())
	}
	return joinComma(names)
}

func isCookieMessage(m *gotgbot.Message) bool {
	return m.Text != "" && m.Text[0] != '/'
}

func isDocumentMessage(m *gotgbot.Message) bool {
	return m.Document != nil
}

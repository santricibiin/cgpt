package telegram

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/verssache/chatgpt-creator/internal/config"
	"github.com/verssache/chatgpt-creator/internal/register"
)

type step int

const (
	stepIdle step = iota
	stepAskTotal
	stepAskWorkers
	stepAskProxy
	stepAskPassword
	stepAskDomain
	stepRunning
)

type session struct {
	step         step
	total        int
	workers      int
	proxy        string
	password     string
	domain       string
	lastBotMsgID int
}

type Bot struct {
	api      *tgbotapi.BotAPI
	cfg      *config.Config
	mu       sync.Mutex
	sessions map[int64]*session
}

func New(token string, cfg *config.Config) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, err
	}
	return &Bot{
		api:      api,
		cfg:      cfg,
		sessions: make(map[int64]*session),
	}, nil
}

func (b *Bot) Start() {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := b.api.GetUpdatesChan(u)

	fmt.Printf("[Telegram] Bot @%s aktif, menunggu perintah /run\n", b.api.Self.UserName)

	for update := range updates {
		if update.Message == nil {
			continue
		}
		go b.handle(update.Message)
	}
}

func (b *Bot) send(chatID int64, text string) int {
	msg := tgbotapi.NewMessage(chatID, text)
	sent, _ := b.api.Send(msg)
	return sent.MessageID
}

func (b *Bot) del(chatID int64, msgID int) {
	if msgID == 0 {
		return
	}
	b.api.Request(tgbotapi.NewDeleteMessage(chatID, msgID))
}

func (b *Bot) edit(chatID int64, msgID int, text string) {
	if msgID == 0 {
		return
	}
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	b.api.Send(edit)
}

func progressBar(done, total int) string {
	const barLen = 20
	pct := done * 100 / total
	filled := barLen * done / total
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barLen-filled)
	return fmt.Sprintf("⏳ Membuat akun...\n[%s] %d%%\n✅ %d / %d berhasil", bar, pct, done, total)
}

func (b *Bot) handle(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	text := strings.TrimSpace(msg.Text)

	b.mu.Lock()
	sess, ok := b.sessions[chatID]
	b.mu.Unlock()

	if msg.IsCommand() && msg.Command() == "run" {
		if ok && sess.step == stepRunning {
			sent := b.send(chatID, "⏳ Sedang berjalan, tunggu selesai dulu.")
			go func() {
				b.del(chatID, msg.MessageID)
				b.del(chatID, sent)
			}()
			return
		}
		b.del(chatID, msg.MessageID)
		msgID := b.send(chatID, "Total akun yang ingin dibuat:")
		b.mu.Lock()
		b.sessions[chatID] = &session{step: stepAskTotal, lastBotMsgID: msgID}
		b.mu.Unlock()
		return
	}

	if !ok || sess.step == stepIdle || sess.step == stepRunning {
		return
	}

	prevBotMsg := sess.lastBotMsgID
	userMsg := msg.MessageID

	switch sess.step {
	case stepAskTotal:
		n, err := strconv.Atoi(text)
		if err != nil || n < 1 {
			b.del(chatID, userMsg)
			sess.lastBotMsgID = b.send(chatID, "❌ Masukkan angka valid.")
			return
		}
		b.del(chatID, prevBotMsg)
		b.del(chatID, userMsg)
		sess.total = n
		sess.step = stepAskWorkers
		sess.lastBotMsgID = b.send(chatID, "Max workers (default: 3, atau ketik angka):")

	case stepAskWorkers:
		if text == "." || text == "" {
			sess.workers = 3
		} else {
			n, err := strconv.Atoi(text)
			if err != nil || n < 1 {
				b.del(chatID, userMsg)
				sess.lastBotMsgID = b.send(chatID, "❌ Masukkan angka valid atau '.' untuk default (3).")
				return
			}
			sess.workers = n
		}
		b.del(chatID, prevBotMsg)
		b.del(chatID, userMsg)
		sess.step = stepAskProxy
		sess.lastBotMsgID = b.send(chatID, "Proxy (contoh: http://user:pass@host:port, atau '.' untuk skip):")

	case stepAskProxy:
		if text == "." {
			sess.proxy = b.cfg.Proxy
		} else {
			sess.proxy = text
		}
		b.del(chatID, prevBotMsg)
		b.del(chatID, userMsg)
		sess.step = stepAskPassword
		if b.cfg.DefaultPassword != "" {
			sess.lastBotMsgID = b.send(chatID, fmt.Sprintf("Password (config: %s, atau '.' untuk pakai):", b.cfg.DefaultPassword))
		} else {
			sess.lastBotMsgID = b.send(chatID, "Password min 12 karakter, atau '.' untuk random:")
		}

	case stepAskPassword:
		if text == "." {
			sess.password = b.cfg.DefaultPassword
		} else {
			if len(text) < 12 {
				b.del(chatID, userMsg)
				sess.lastBotMsgID = b.send(chatID, "❌ Password minimal 12 karakter atau '.' untuk random.")
				return
			}
			sess.password = text
		}
		b.del(chatID, prevBotMsg)
		b.del(chatID, userMsg)
		sess.step = stepAskDomain
		if b.cfg.DefaultDomain != "" {
			sess.lastBotMsgID = b.send(chatID, fmt.Sprintf("Domain email (config: %s, atau '.' untuk pakai):", b.cfg.DefaultDomain))
		} else {
			sess.lastBotMsgID = b.send(chatID, "Domain email, atau '.' untuk random (generator.email):")
		}

	case stepAskDomain:
		if text == "." {
			sess.domain = b.cfg.DefaultDomain
		} else {
			sess.domain = text
		}
		b.del(chatID, prevBotMsg)
		b.del(chatID, userMsg)
		sess.step = stepRunning
		sess.lastBotMsgID = b.send(chatID, fmt.Sprintf("🚀 Memulai registrasi %d akun dengan %d worker...", sess.total, sess.workers))
		go b.run(chatID, sess)
	}
}

func (b *Bot) run(chatID int64, sess *session) {
	outFile := b.cfg.OutputFile
	runningMsgID := sess.lastBotMsgID

	lastPct := -1
	register.RunBatchWithProgress(sess.total, outFile, sess.workers, sess.proxy, sess.password, sess.domain, func(done, total int) {
		pct := done * 100 / total
		if pct != lastPct {
			lastPct = pct
			b.edit(chatID, runningMsgID, progressBar(done, total))
		}
	})

	b.mu.Lock()
	delete(b.sessions, chatID)
	b.mu.Unlock()

	b.del(chatID, runningMsgID)

	f, err := os.Open(outFile)
	if err != nil {
		b.send(chatID, "✅ Selesai, tapi file hasil tidak ditemukan.")
		return
	}

	doc := tgbotapi.NewDocument(chatID, tgbotapi.FileReader{
		Name:   "results.txt",
		Reader: f,
	})
	doc.Caption = fmt.Sprintf("✅ Selesai! %d akun telah dibuat.", sess.total)
	b.api.Send(doc)
	f.Close()

	os.Remove(outFile)
}

package wechat

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"orca-wails/internal/tasks"
)

type Transport interface {
	StartQR(context.Context) (QR, error)
	PollQR(context.Context, string, string, string) (Scan, error)
	Updates(context.Context, Account, string) ([]Inbound, string, error)
	Send(context.Context, Account, string, string, string) error
}

type TaskSource struct {
	List     func(context.Context) ([]tasks.Row, error)
	Prompt   func(context.Context, tasks.Row) (tasks.Prompt, error)
	Answer   func(context.Context, tasks.Row, tasks.Answer) error
	FollowUp func(context.Context, tasks.Row, string) error
	Result   func(context.Context, tasks.Row) string
}

var ErrPromptSettled = errors.New("the original prompt has already been settled")

// Status is the entire desktop-facing state. The iLink credentials never cross the tray bridge.
type Status struct {
	Phase        string `json:"phase"`
	QR           string `json:"qr,omitempty"`
	Code         string `json:"code,omitempty"`
	Message      string `json:"message,omitempty"`
	LastReceived int64  `json:"lastReceived,omitempty"`
	LastSent     int64  `json:"lastSent,omitempty"`
}

type Service struct {
	root      context.Context
	path      string
	transport Transport
	tasks     TaskSource
	changed   func()

	mu       sync.Mutex
	status   Status
	cancel   context.CancelFunc
	pin      chan string
	test     chan struct{}
	refresh  chan struct{}
	restored *profile
	epoch    uint64
}

type profile struct {
	Epoch         uint64
	Account       Account
	BoundAt       int64
	ContextToken  string
	Cursor        string
	Received      []string
	Notified      map[string]int64
	Completions   map[string]string
	TaskCodes     map[string]string
	Prompts       map[string]savedPrompt
	NextTask      int
	NextPrompt    int
	LastReceived  int64
	LastSent      int64
	DeliveryIssue string
}

type savedPrompt struct {
	TaskID string
	Draft  ReplyDraft
}

func Open(ctx context.Context, path string, transport Transport, source TaskSource, changed func()) *Service {
	service := &Service{root: ctx, path: path, transport: transport, tasks: source, changed: changed, status: Status{Phase: "unbound"}}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return service
	}
	p := &profile{}
	if err != nil || len(body) > 8<<20 || json.Unmarshal(body, p) != nil {
		service.status.Message = "无法恢复微信连接，请重新扫码。"
		return service
	}
	service.epoch = p.Epoch
	if p.Account == (Account{}) {
		return service
	}
	if p.Account.Token == "" || p.Account.BotID == "" || p.Account.OwnerID == "" || p.BoundAt == 0 || p.ContextToken == "" {
		service.status.Message = "无法恢复微信连接，请重新扫码。"
		return service
	}
	p.initMaps()
	service.restored = p
	service.status = Status{Phase: "reconnecting", LastSent: p.LastSent, LastReceived: p.LastReceived}
	return service
}

func (s *Service) Resume() {
	s.mu.Lock()
	if s.restored == nil {
		s.mu.Unlock()
		return
	}
	p := s.restored
	s.restored = nil
	ctx, _, test, refresh := s.startWorker()
	s.mu.Unlock()
	go s.runAccount(ctx, p, "", test, refresh)
}

func (s *Service) Snapshot() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Service) update(ctx context.Context, state Status) {
	s.mu.Lock()
	if ctx.Err() != nil || s.status == state {
		s.mu.Unlock()
		return
	}
	s.status = state
	s.mu.Unlock()
	s.changed()
}

func (s *Service) startWorker() (context.Context, <-chan string, <-chan struct{}, <-chan struct{}) {
	if s.cancel != nil {
		s.cancel()
	}
	ctx, cancel := context.WithCancel(s.root)
	s.cancel = cancel
	s.pin, s.test, s.refresh = make(chan string, 1), make(chan struct{}, 1), make(chan struct{}, 1)
	return ctx, s.pin, s.test, s.refresh
}

func (s *Service) StartPair() {
	s.mu.Lock()
	if s.status.Phase != "unbound" && s.status.Phase != "expired" {
		s.mu.Unlock()
		return
	}
	ctx, pin, test, refresh := s.startWorker()
	s.restored = nil
	s.status = Status{Phase: "starting"}
	s.mu.Unlock()
	s.changed()
	go s.scan(ctx, pin, test, refresh)
}

func (s *Service) SubmitPIN(value string) {
	value = strings.TrimSpace(value)
	if len(value) < 4 || len(value) > 8 {
		return
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Phase == "pin" {
		select {
		case s.pin <- value:
		default:
		}
	}
}

func (s *Service) Test() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Phase == "connected" {
		select {
		case s.test <- struct{}{}:
		default:
		}
	}
}

func (s *Service) Refresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case s.refresh <- struct{}{}:
	default:
	}
}

func (s *Service) Disconnect() error {
	s.mu.Lock()
	// Keep only the non-secret binding number so old replies cannot match a
	// new task even when the same WeChat owner scans again.
	if err := s.writeProfile(&profile{Epoch: s.epoch}); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.restored = nil
	s.status = Status{Phase: "unbound"}
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Service) scan(session context.Context, pins <-chan string, test, refresh <-chan struct{}) {
	ctx, cancel := context.WithTimeout(session, 4*time.Minute)
	defer func() {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			s.pairFailed(session, "二维码已过期，请重新扫码。")
		}
		cancel()
	}()
	qr, err := s.transport.StartQR(ctx)
	if err != nil {
		s.pairFailed(ctx, "获取微信二维码失败，请重试。")
		return
	}
	s.update(ctx, Status{Phase: "scan", QR: qr.Content})
	var redirect, pin string
	for ctx.Err() == nil {
		result, err := s.transport.PollQR(ctx, qr.ID, pin, redirect)
		if err != nil {
			s.pairFailed(ctx, "读取扫码状态失败，请重新扫码。")
			return
		}
		pin = ""
		switch {
		case strings.Contains(result.Status, "need_verifycode"):
			s.update(ctx, Status{Phase: "pin"})
			select {
			case pin = <-pins:
				s.update(ctx, Status{Phase: "starting", Message: "正在验证微信验证码…"})
			case <-ctx.Done():
				return
			}
		case result.Status == "wait":
			if !pause(ctx, 300*time.Millisecond) {
				return
			}
		case result.Status == "scaned" || result.Status == "scaned_but_redirect":
			if result.RedirectHost != "" {
				redirect = result.RedirectHost
			}
			s.update(ctx, Status{Phase: "verify"})
		case result.Status == "confirmed":
			code, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
			if err != nil {
				s.pairFailed(ctx, "生成配对码失败，请重新扫码。")
				return
			}
			p := &profile{Account: result.Account}
			p.initMaps()
			s.update(ctx, Status{Phase: "verify", Code: fmt.Sprintf("%06d", code.Int64())})
			cancel()
			s.runAccount(session, p, fmt.Sprintf("%06d", code.Int64()), test, refresh)
			return
		case result.Status == "expired" || result.Status == "verify_code_blocked":
			s.pairFailed(ctx, "二维码已过期，请重新扫码。")
			return
		case result.Status == "binded_redirect":
			s.pairFailed(ctx, "微信显示此 Bot 已绑定。请从微信解除旧绑定后重试。")
			return
		default:
			s.pairFailed(ctx, "微信返回了未识别的绑定状态，请重新扫码。")
			return
		}
	}
}

func (s *Service) pairFailed(ctx context.Context, message string) {
	s.update(ctx, Status{Phase: "expired", Message: message})
}

func pause(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (p *profile) initMaps() {
	if p.Notified == nil {
		p.Notified = make(map[string]int64)
	}
	if p.Completions == nil {
		p.Completions = make(map[string]string)
	}
	if p.TaskCodes == nil {
		p.TaskCodes = make(map[string]string)
	}
	if p.Prompts == nil {
		p.Prompts = make(map[string]savedPrompt)
	}
}

func (s *Service) save(ctx context.Context, p *profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.BoundAt == 0 {
		return nil
	}
	if p.Epoch == 0 {
		p.Epoch = s.epoch + 1
		if err := s.writeProfile(p); err != nil {
			p.Epoch = 0
			return err
		}
		s.epoch = p.Epoch
		return nil
	}
	return s.writeProfile(p)
}

func (s *Service) writeProfile(p *profile) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(s.path), ".wechat-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(body)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), s.path)
}

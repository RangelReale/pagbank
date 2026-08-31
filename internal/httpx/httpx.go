// Package httpx é o cliente HTTP compartilhado pelas fontes do PagBank.
//
// Ele resolve três coisas que as duas APIs exigem: repetir requisições que
// falharam por limite de taxa ou erro transitório, espaçar as chamadas (a
// extração de um mês no EDI são dezenas de requisições em sequência) e garantir
// que token e e-mail nunca vazem para log ou mensagem de erro — a API legada
// carrega o token na própria query string.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Redactor troca segredos conhecidos por um marcador antes que qualquer texto
// chegue ao usuário.
type Redactor struct {
	secrets []string
}

// NewRedactor registra os valores a esconder. Strings vazias e muito curtas são
// ignoradas: substituí-las estragaria a mensagem sem proteger nada.
func NewRedactor(secrets ...string) *Redactor {
	r := &Redactor{}
	for _, s := range secrets {
		if len(s) >= 4 {
			r.secrets = append(r.secrets, s)
		}
	}
	return r
}

// String devolve o texto com os segredos substituídos.
func (r *Redactor) String(s string) string {
	if r == nil {
		return s
	}
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, "***")
		// O token vai na query string da API legada, onde chega codificado.
		if enc := url.QueryEscape(secret); enc != secret {
			s = strings.ReplaceAll(s, enc, "***")
		}
	}
	return s
}

// Error reescreve a mensagem do erro sem os segredos. O erro original não é
// encadeado: ele guarda o texto cru e um %w o traria de volta à tona.
func (r *Redactor) Error(err error) error {
	if err == nil {
		return nil
	}
	msg := r.String(err.Error())
	if msg == err.Error() {
		return err
	}
	return errors.New(msg)
}

// Response é a resposta já lida por inteiro. O corpo é materializado porque uma
// tentativa que falhou precisa ser descartada e refeita, e as páginas do PagBank
// têm no máximo mil registros.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// StatusError é uma resposta HTTP que não é 2xx.
type StatusError struct {
	StatusCode int
	Status     string
	URL        string // já redigido
	Body       string // já redigido e truncado

	// retryAfter é o Retry-After da resposta, quando o servidor mandou um.
	retryAfter time.Duration
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("%s respondeu %s", e.URL, e.Status)
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

const maxErrorBody = 512

// Client executa GETs com retry, espaçamento e redação.
type Client struct {
	HTTP        *http.Client
	UserAgent   string
	MaxAttempts int           // tentativas totais, não repetições extras
	BaseDelay   time.Duration // espera antes da primeira repetição
	MaxDelay    time.Duration
	MinInterval time.Duration // espaçamento mínimo entre requisições
	Redactor    *Redactor
	// Logf, quando definido, recebe uma linha por tentativa. O texto já passou
	// pelo Redactor.
	Logf func(format string, args ...any)

	mu   sync.Mutex
	last time.Time
}

// New devolve um Client com padrões adequados às APIs do PagBank.
func New(r *Redactor) *Client {
	return &Client{
		HTTP:        &http.Client{Timeout: 60 * time.Second},
		UserAgent:   "pagbank-extract/1.0 (+https://github.com/RangelReale/pagbank)",
		MaxAttempts: 4,
		BaseDelay:   time.Second,
		MaxDelay:    30 * time.Second,
		MinInterval: 200 * time.Millisecond,
		Redactor:    r,
	}
}

func (c *Client) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf("%s", c.Redactor.String(fmt.Sprintf(format, args...)))
	}
}

// Get busca a URL, repetindo enquanto o erro parecer transitório. O contexto
// cancela tanto a requisição quanto a espera entre tentativas.
func (c *Client) Get(ctx context.Context, u string, header http.Header) (*Response, error) {
	safeURL := c.Redactor.String(u)

	var lastErr error
	for attempt := 1; attempt <= c.MaxAttempts; attempt++ {
		if attempt > 1 {
			delay := c.backoff(attempt, lastErr)
			c.logf("tentativa %d/%d para %s em %s (motivo: %v)", attempt, c.MaxAttempts, safeURL, delay.Round(time.Millisecond), lastErr)
			if err := sleep(ctx, delay); err != nil {
				return nil, err
			}
		}
		if err := c.pace(ctx); err != nil {
			return nil, err
		}

		resp, err := c.attempt(ctx, u, header, safeURL)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !retryable(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("desisti após %d tentativas: %w", c.MaxAttempts, lastErr)
}

func (c *Client) attempt(ctx context.Context, u string, header http.Header, safeURL string) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, c.Redactor.Error(err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, c.Redactor.Error(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("lendo resposta de %s: %w", safeURL, c.Redactor.Error(err))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &StatusError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			URL:        safeURL,
			Body:       c.Redactor.String(truncate(strings.TrimSpace(string(body)), maxErrorBody)),
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	return &Response{StatusCode: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

// pace segura a próxima requisição até MinInterval desde a anterior.
func (c *Client) pace(ctx context.Context) error {
	if c.MinInterval <= 0 {
		return nil
	}
	c.mu.Lock()
	wait := time.Until(c.last.Add(c.MinInterval))
	c.last = time.Now().Add(max(wait, 0))
	c.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	return sleep(ctx, wait)
}

// backoff é exponencial com jitter, mas o Retry-After do servidor tem prioridade.
func (c *Client) backoff(attempt int, err error) time.Duration {
	var se *StatusError
	if errors.As(err, &se) && se.retryAfter > 0 {
		return min(se.retryAfter, c.MaxDelay)
	}
	d := c.BaseDelay << (attempt - 2)
	d = min(d, c.MaxDelay)
	// Jitter de ±25% para não sincronizar repetições.
	jitter := time.Duration(rand.Int64N(int64(d/2)+1)) - d/4
	return max(d+jitter, 0)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// retryable diz se vale a pena repetir. Erros de rede e de leitura entram;
// entre os status HTTP, só 429 e os 5xx que não sejam "não implementado".
func retryable(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		switch {
		case se.StatusCode == http.StatusTooManyRequests:
			return true
		case se.StatusCode == http.StatusNotImplemented:
			return false
		case se.StatusCode >= 500:
			return true
		}
		return false
	}
	return true
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

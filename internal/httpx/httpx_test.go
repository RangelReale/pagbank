package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testClient devolve um Client rápido o bastante para não travar o teste.
func testClient(r *Redactor) *Client {
	c := New(r)
	c.BaseDelay = time.Millisecond
	c.MaxDelay = 5 * time.Millisecond
	c.MinInterval = 0
	return c
}

func TestGetRepeteEm500EDepoisTemSucesso(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			http.Error(w, "instabilidade", http.StatusBadGateway)
			return
		}
		w.Header().Set("VALIDADO", "TRUE")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	resp, err := testClient(nil).Get(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := string(resp.Body); got != `{"ok":true}` {
		t.Errorf("corpo = %q", got)
	}
	if resp.Header.Get("VALIDADO") != "TRUE" {
		t.Errorf("header VALIDADO perdido")
	}
	if n.Load() != 3 {
		t.Errorf("tentativas = %d, quero 3", n.Load())
	}
}

func TestGetNaoRepeteEm401(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Error(w, "credenciais invalidas", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := testClient(nil).Get(context.Background(), srv.URL, nil)
	if err == nil {
		t.Fatal("esperava erro")
	}
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusUnauthorized {
		t.Fatalf("erro = %v, quero StatusError 401", err)
	}
	if n.Load() != 1 {
		t.Errorf("tentativas = %d, quero 1 — 401 não é transitório", n.Load())
	}
}

func TestGetDesisteDepoisDeMaxAttempts(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Error(w, "fora do ar", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := testClient(nil)
	c.MaxAttempts = 3
	_, err := c.Get(context.Background(), srv.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "desisti após 3 tentativas") {
		t.Fatalf("erro = %v", err)
	}
	if n.Load() != 3 {
		t.Errorf("tentativas = %d, quero 3", n.Load())
	}
}

func TestGetRespeitaRetryAfter(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "devagar", http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	c := testClient(nil)
	c.MaxDelay = 30 * time.Millisecond // teto: o Retry-After de 1s é cortado aqui

	inicio := time.Now()
	if _, err := c.Get(context.Background(), srv.URL, nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Sem honrar o Retry-After a espera seria BaseDelay (1ms); com ele, o teto.
	if d := time.Since(inicio); d < 20*time.Millisecond {
		t.Errorf("esperou %v, quero pelo menos o teto de %v", d, c.MaxDelay)
	}
}

func TestGetCancelaComOContexto(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fora do ar", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := testClient(nil)
	// A espera entre tentativas é onde o cancelamento tem que pegar.
	c.BaseDelay, c.MaxDelay = time.Hour, time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	inicio := time.Now()
	if _, err := c.Get(ctx, srv.URL, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("erro = %v, quero context.Canceled", err)
	}
	if d := time.Since(inicio); d > 5*time.Second {
		t.Errorf("cancelamento demorou %v", d)
	}
}

func TestMinIntervalEspacaAsRequisicoes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	c := testClient(nil)
	c.MinInterval = 30 * time.Millisecond

	inicio := time.Now()
	for range 3 {
		if _, err := c.Get(context.Background(), srv.URL, nil); err != nil {
			t.Fatalf("Get: %v", err)
		}
	}
	if d := time.Since(inicio); d < 60*time.Millisecond {
		t.Errorf("três requisições em %v, quero ao menos 2x%v", d, c.MinInterval)
	}
}

func TestErroNaoVazaOToken(t *testing.T) {
	const token = "3B7B1B4C-secreto-1234"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A API legada devolve o token de volta na mensagem de erro.
		http.Error(w, "token invalido: "+r.URL.Query().Get("token"), http.StatusBadRequest)
	}))
	defer srv.Close()

	c := testClient(NewRedactor(token))
	var logado strings.Builder
	c.Logf = func(format string, args ...any) { fmt.Fprintf(&logado, format, args...) }

	u := srv.URL + "/v2/transactions?email=a%40b.com&token=" + token
	_, err := c.Get(context.Background(), u, nil)
	if err == nil {
		t.Fatal("esperava erro")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("o token vazou no erro: %v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Errorf("erro sem marcador de redação: %v", err)
	}
	if strings.Contains(logado.String(), token) {
		t.Errorf("o token vazou no log: %s", logado.String())
	}
}

func TestRedactorEscapaValoresCodificadosNaURL(t *testing.T) {
	r := NewRedactor("a@b.com", "tok/en+1")
	got := r.String("https://x/?email=a%40b.com&token=tok%2Fen%2B1&t=tok/en+1")
	if strings.Contains(got, "a@b.com") || strings.Contains(got, "a%40b.com") {
		t.Errorf("e-mail vazou: %s", got)
	}
	if strings.Contains(got, "tok%2Fen%2B1") || strings.Contains(got, "tok/en+1") {
		t.Errorf("token vazou: %s", got)
	}
}

func TestRedactorIgnoraSegredosCurtos(t *testing.T) {
	// Um segredo vazio ou de dois caracteres transformaria qualquer mensagem
	// numa sopa de asteriscos.
	r := NewRedactor("", "ab")
	if got := r.String("abacate"); got != "abacate" {
		t.Errorf("String = %q, quero intacto", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("120"); got != 2*time.Minute {
		t.Errorf("segundos: %v", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("vazio: %v", got)
	}
	if got := parseRetryAfter("ontem"); got != 0 {
		t.Errorf("inválido: %v", got)
	}
	futuro := time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(futuro); got <= 0 || got > time.Minute {
		t.Errorf("data HTTP: %v", got)
	}
	passado := time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(passado); got != 0 {
		t.Errorf("data no passado: %v", got)
	}
}
